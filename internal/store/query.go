package store

import (
	"context"
	"database/sql"
	"sort"
)

const (
	overviewBuckets = 180
	maxWindowRows   = 300000
)

type rawPoint struct {
	ts     int64
	series string
	a      *float64
	b      *float64
	c      *float64
	d      *float64
}

type bucketAcc struct {
	ts      int64
	sum     float64
	min     float64
	max     float64
	count   int
	present int
	has     bool
}

func (b *bucketAcc) add(v *float64) {
	b.count++
	if v == nil {
		return
	}
	b.present++
	if !b.has {
		b.min, b.max = *v, *v
		b.has = true
	} else {
		if *v < b.min {
			b.min = *v
		}
		if *v > b.max {
			b.max = *v
		}
	}
	b.sum += *v
}

func (b *bucketAcc) point() Point {
	p := Point{TS: b.ts, Count: b.count}
	if b.has && b.present > 0 {
		avg := b.sum / float64(b.present)
		minv, maxv := b.min, b.max
		p.Avg, p.Min, p.Max = &avg, &minv, &maxv
	}
	return p
}

// QueryWindow returns downsampled series for [from,to]. Every series uses a
// fixed bucket count, and each bucket exposes both average and peak so a short
// spike cannot be hidden by averaging.
func (s *Store) QueryWindow(ctx context.Context, from, to int64, rttTarget string) (Window, error) {
	out := Window{From: from, To: to, Counts: make(map[string]int)}
	if to <= from {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ts_ms, kind, series, a, b, c, d
		FROM metric_samples
		WHERE ts_ms >= ? AND ts_ms <= ?
		  AND (kind <> 'rtt' OR series = ?)
		ORDER BY ts_ms ASC
		LIMIT ?`, from, to, rttTarget, maxWindowRows+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	byKind := make(map[string][]rawPoint)
	total := 0
	for rows.Next() {
		var r rawPoint
		var kind string
		var a, b, c, d sql.NullFloat64
		if err := rows.Scan(&r.ts, &kind, &r.series, &a, &b, &c, &d); err != nil {
			return out, err
		}
		if a.Valid {
			v := a.Float64
			r.a = &v
		}
		if b.Valid {
			v := b.Float64
			r.b = &v
		}
		if c.Valid {
			v := c.Float64
			r.c = &v
		}
		if d.Valid {
			v := d.Float64
			r.d = &v
		}
		byKind[kind] = append(byKind[kind], r)
		out.Counts[kind]++
		total++
		if total > maxWindowRows {
			out.Truncated = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(byKind) == 0 {
		return out, nil
	}

	width := (to - from) / overviewBuckets
	if width < 1 {
		width = 1
	}
	// CPU: A is whole-machine percent.
	out.CPU = downsample(byKind["cpu"], from, width, func(r rawPoint) *float64 { return r.a })

	// Memory: C is used percent; latest A/B are the current byte readings.
	memRows := byKind["mem"]
	if len(memRows) > 0 {
		out.Memory = downsample(memRows, from, width, func(r rawPoint) *float64 { return r.c })
		last := memRows[len(memRows)-1]
		out.MemUsed, out.MemTotal, out.MemPercent = last.a, last.b, last.c
	}

	// Disks: one series per local volume, used percent plus latest bytes.
	disks := groupBySeries(byKind["disk"])
	keys := sortedKeys(disks)
	if len(keys) > 20 {
		keys = keys[:20]
	}
	for _, key := range keys {
		list := disks[key]
		ds := DiskSeries{Series: key}
		ds.Points = downsample(list, from, width, func(r rawPoint) *float64 { return r.c })
		last := list[len(list)-1]
		ds.Used, ds.Total, ds.Percent = last.a, last.b, last.c
		out.Disks = append(out.Disks, ds)
	}

	// Disk I/O: B/s and IOPS, preserving per-bucket peaks.
	ios := groupBySeries(byKind["diskio"])
	ioKeys := sortedKeys(ios)
	if len(ioKeys) > 20 {
		ioKeys = ioKeys[:20]
	}
	for _, key := range ioKeys {
		list := ios[key]
		io := IOSeries{Series: key}
		io.Read = downsample(list, from, width, func(r rawPoint) *float64 { return r.a })
		io.Write = downsample(list, from, width, func(r rawPoint) *float64 { return r.b })
		last := list[len(list)-1]
		io.LastRead, io.LastWrite, io.ReadIOPS, io.WriteIOPS = last.a, last.b, last.c, last.d
		out.DiskIO = append(out.DiskIO, io)
	}

	// RTT: average and maximum response plus packet loss, one bucket per time.
	out.RTT = rttPoints(byKind["rtt"], from, width)
	return out, nil
}

func downsample(rows []rawPoint, from, width int64, value func(rawPoint) *float64) []Point {
	if len(rows) == 0 {
		return nil
	}
	buckets := make(map[int64]*bucketAcc)
	for _, r := range rows {
		idx := (r.ts - from) / width
		if idx < 0 {
			idx = 0
		}
		if idx >= overviewBuckets {
			idx = overviewBuckets - 1
		}
		ts := from + idx*width
		b := buckets[ts]
		if b == nil {
			b = &bucketAcc{ts: ts}
			buckets[ts] = b
		}
		b.add(value(r))
	}
	out := make([]Point, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, b.point())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

func groupBySeries(rows []rawPoint) map[string][]rawPoint {
	out := make(map[string][]rawPoint)
	for _, r := range rows {
		out[r.series] = append(out[r.series], r)
	}
	return out
}

func sortedKeys(m map[string][]rawPoint) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func rttPoints(rows []rawPoint, from, width int64) []RTTPoint {
	if len(rows) == 0 {
		return nil
	}
	type acc struct {
		ts              int64
		avgSum, lossSum float64
		avgN, lossN     int
		maxVal          float64
		maxSet          bool
	}
	buckets := make(map[int64]*acc)
	for _, r := range rows {
		idx := (r.ts - from) / width
		if idx < 0 {
			idx = 0
		}
		if idx >= overviewBuckets {
			idx = overviewBuckets - 1
		}
		ts := from + idx*width
		b := buckets[ts]
		if b == nil {
			b = &acc{ts: ts}
			buckets[ts] = b
		}
		if r.a != nil {
			b.avgSum += *r.a
			b.avgN++
		}
		if r.b != nil && (!b.maxSet || *r.b > b.maxVal) {
			b.maxVal = *r.b
			b.maxSet = true
		}
		if r.c != nil {
			b.lossSum += *r.c
			b.lossN++
		}
	}
	out := make([]RTTPoint, 0, len(buckets))
	for _, b := range buckets {
		p := RTTPoint{TS: b.ts}
		if b.avgN > 0 {
			v := b.avgSum / float64(b.avgN)
			p.Avg = &v
		}
		if b.maxSet {
			v := b.maxVal
			p.Max = &v
		}
		if b.lossN > 0 {
			v := b.lossSum / float64(b.lossN)
			p.Loss = &v
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out
}

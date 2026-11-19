//go:build windows

package winmetrics

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")
	modPSAPI    = syscall.NewLazyDLL("psapi.dll")
	modIPHLPAPI = syscall.NewLazyDLL("iphlpapi.dll")

	procGetSystemTimes           = modKernel32.NewProc("GetSystemTimes")
	procGetActiveProcessorCount  = modKernel32.NewProc("GetActiveProcessorCount")
	procGlobalMemoryStatusEx     = modKernel32.NewProc("GlobalMemoryStatusEx")
	procCreateToolhelp32Snapshot = modKernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = modKernel32.NewProc("Process32FirstW")
	procProcess32NextW           = modKernel32.NewProc("Process32NextW")
	procOpenProcess              = modKernel32.NewProc("OpenProcess")
	procGetProcessTimes          = modKernel32.NewProc("GetProcessTimes")
	procGetProcessMemoryInfo     = modPSAPI.NewProc("GetProcessMemoryInfo")
	procCloseHandle              = modKernel32.NewProc("CloseHandle")
	procCreateFileW              = modKernel32.NewProc("CreateFileW")
	procDeviceIoControl          = modKernel32.NewProc("DeviceIoControl")
	procGetLastError             = modKernel32.NewProc("GetLastError")
	procIcmpCreateFile           = modIPHLPAPI.NewProc("IcmpCreateFile")
	procIcmp6CreateFile          = modIPHLPAPI.NewProc("Icmp6CreateFile")
	procIcmp6SendEcho2           = modIPHLPAPI.NewProc("Icmp6SendEcho2")
	procIcmpCloseHandle          = modIPHLPAPI.NewProc("IcmpCloseHandle")
	procIcmpSendEcho             = modIPHLPAPI.NewProc("IcmpSendEcho")
)

const (
	th32csSnapProcess             = 0x00000002
	processQueryLimitedInfo       = 0x1000
	processQueryInformation       = 0x0400
	processVMRead                 = 0x0010
	genericRead                   = 0x80000000
	invalidHandleValue            = ^uintptr(0)
	fileShareRead                 = 0x00000001
	fileShareWrite                = 0x00000002
	openExisting                  = 3
	ioctlDiskPerformance          = 0x00070020
	icmpSuccess                   = 0
	icmpDestinationHostUnreach    = 11003
	icmpRequestTimedOut           = 11010
	icmpDestinationNetUnreachable = 11002
	icmpTTLExpiredTransit         = 11013
	icmpGeneralFailure            = 11050
)

type filetime struct {
	LowDateTime  uint32
	HighDateTime uint32
}

func (f filetime) counter() uint64 {
	return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime)
}

func (f filetime) unixMS() int64 {
	n := f.counter()
	const windowsToUnix100ns = 11644473600000000
	if n == 0 || n < windowsToUnix100ns {
		return 0
	}
	return int64((n - windowsToUnix100ns) / 10000)
}

func filetimeFrom100ns(v uint64) filetime {
	return filetime{LowDateTime: uint32(v), HighDateTime: uint32(v >> 32)}
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

type processMemoryCounters struct {
	CB                    uint32
	PageFaultCount        uint32
	PeakWorkingSetSize    uintptr
	WorkingSetSize        uintptr
	QuotaPeakPagedPool    uintptr
	QuotaPagedPool        uintptr
	QuotaPeakNonPagedPool uintptr
	QuotaNonPagedPool     uintptr
	PagefileUsage         uintptr
	PeakPagefileUsage     uintptr
	PrivateUsage          uintptr
}

// diskPerformance mirrors DISK_PERFORMANCE from winioctl.h. LARGE_INTEGER is
// represented by int64; the layout is 88 bytes on both 32- and 64-bit Windows.
type diskPerformance struct {
	BytesRead           int64
	BytesWritten        int64
	ReadTime            int64
	WriteTime           int64
	IdleTime            int64
	ReadCount           uint32
	WriteCount          uint32
	QueueDepth          uint32
	SplitCount          uint32
	QueryTime           int64
	StorageDeviceNumber uint32
	StorageManagerName  [8]uint16
}

type rawProcess struct {
	PID        int32
	PPID       int32
	Name       string
	StartMS    int64
	CPU100ns   uint64
	WorkingSet *float64
}

func activeProcessorCount() int {
	r1, _, _ := procGetActiveProcessorCount.Call(0xffff) // ALL_PROCESSOR_GROUPS
	if r1 == 0 || r1 > 4096 {
		return 1
	}
	return int(r1)
}

func readSystemTimes() (cpuCounters, error) {
	var i, k, u filetime
	r1, _, e := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&i)),
		uintptr(unsafe.Pointer(&k)),
		uintptr(unsafe.Pointer(&u)),
	)
	if r1 == 0 {
		return cpuCounters{}, callError("GetSystemTimes", e)
	}
	return cpuCounters{idle: i.counter(), kernel: k.counter(), user: u.counter(), valid: true}, nil
}

func readMemory() (used, total, percent float64, err error) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r1, _, e := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r1 == 0 {
		return 0, 0, 0, callError("GlobalMemoryStatusEx", e)
	}
	if m.TotalPhys == 0 {
		return 0, 0, 0, errors.New("Windows 返回的内存总量为 0")
	}
	total = float64(m.TotalPhys)
	avail := m.AvailPhys
	if avail > m.TotalPhys {
		avail = m.TotalPhys
	}
	used = float64(m.TotalPhys - avail)
	percent = used / total * 100
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	return used, total, percent, nil
}

func snapshotProcessEntries() ([]processEntry32, error) {
	h, _, e := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if h == invalidHandleValue {
		return nil, callError("CreateToolhelp32Snapshot", e)
	}
	handle := syscall.Handle(h)
	defer procCloseHandle.Call(uintptr(handle))
	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r1, _, e := procProcess32FirstW.Call(uintptr(handle), uintptr(unsafe.Pointer(&entry)))
	if r1 == 0 {
		if isErrno(e, 18) {
			return nil, nil
		}
		return nil, callError("Process32FirstW", e)
	}
	out := make([]processEntry32, 0, 256)
	for {
		cp := entry
		out = append(out, cp)
		r1, _, e = procProcess32NextW.Call(uintptr(handle), uintptr(unsafe.Pointer(&entry)))
		if r1 == 0 {
			// ERROR_NO_MORE_FILES (18) is the normal end of enumeration.
			if errno, ok := e.(syscall.Errno); ok && errno == 18 {
				break
			}
			if e != nil && !isErrno(e, 18) {
				return out, callError("Process32NextW", e)
			}
			break
		}
	}
	return out, nil
}

func queryProcess(pid uint32, sid *windows.SID) (startMS int64, cpu100ns uint64, workingSet, privateCommit *float64, err error) {
	r1, _, e := procOpenProcess.Call(processQueryInformation|processVMRead, 0, uintptr(pid))
	if r1 == 0 {
		r1, _, e = procOpenProcess.Call(processQueryLimitedInfo|processVMRead, 0, uintptr(pid))
	}
	if r1 == 0 {
		r1, _, e = procOpenProcess.Call(processQueryInformation, 0, uintptr(pid))
	}
	if r1 == 0 {
		r1, _, e = procOpenProcess.Call(processQueryLimitedInfo, 0, uintptr(pid))
	}
	if r1 == 0 {
		return 0, 0, nil, nil, callError("OpenProcess", e)
	}
	h := syscall.Handle(r1)
	defer procCloseHandle.Call(uintptr(h))
	var token windows.Token
	if err := windows.OpenProcessToken(windows.Handle(h), windows.TOKEN_QUERY, &token); err != nil {
		return 0, 0, nil, nil, err
	}
	defer token.Close()
	userInfo, err := token.GetTokenUser()
	if err != nil {
		return 0, 0, nil, nil, err
	}
	if !sid.Equals(userInfo.User.Sid) {
		return 0, 0, nil, nil, syscall.ERROR_ACCESS_DENIED
	}

	var creation, exit, kernel, user filetime
	r1, _, e = procGetProcessTimes.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)),
	)
	if r1 == 0 {
		return 0, 0, nil, nil, callError("GetProcessTimes", e)
	}
	startMS = creation.unixMS()
	cpu100ns = kernel.counter() + user.counter()

	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r1, _, e = procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB))
	if r1 != 0 {
		v := float64(pmc.WorkingSetSize)
		workingSet = &v
		committed := float64(pmc.PrivateUsage)
		privateCommit = &committed
	}
	return startMS, cpu100ns, workingSet, privateCommit, nil
}

type diskCounters struct {
	readBytes  float64
	writeBytes float64
	readCount  float64
	writeCount float64
	readTime   float64
	writeTime  float64
	disks      int
}

func readDiskCounters() (diskCounters, error) {
	var total diskCounters
	var lastErr error
	for i := 0; i < 32; i++ {
		path, err := syscall.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, i))
		if err != nil {
			continue
		}
		h, _, e := procCreateFileW.Call(
			uintptr(unsafe.Pointer(path)),
			0,
			fileShareRead|fileShareWrite,
			0,
			openExisting,
			0,
			0,
		)
		if h == invalidHandleValue {
			// Some Windows builds require read access for
			// IOCTL_DISK_PERFORMANCE; retry once before declaring the counter
			// unavailable.
			h, _, e = procCreateFileW.Call(
				uintptr(unsafe.Pointer(path)),
				genericRead,
				fileShareRead|fileShareWrite,
				0,
				openExisting,
				0,
				0,
			)
		}
		if h == invalidHandleValue {
			lastErr = callError("CreateFileW", e)
			continue
		}
		var dp diskPerformance
		var returned uint32
		r1, _, e := procDeviceIoControl.Call(
			h,
			ioctlDiskPerformance,
			0, 0,
			uintptr(unsafe.Pointer(&dp)),
			unsafe.Sizeof(dp),
			uintptr(unsafe.Pointer(&returned)),
			0,
		)
		procCloseHandle.Call(h)
		if r1 == 0 {
			lastErr = callError("DeviceIoControl(IOCTL_DISK_PERFORMANCE)", e)
			continue
		}
		total.readBytes += float64(dp.BytesRead)
		total.writeBytes += float64(dp.BytesWritten)
		total.readCount += float64(dp.ReadCount)
		total.writeCount += float64(dp.WriteCount)
		total.readTime += float64(dp.ReadTime)
		total.writeTime += float64(dp.WriteTime)
		total.disks++
	}
	if total.disks == 0 {
		if lastErr == nil {
			lastErr = errors.New("没有可读取性能计数的物理磁盘")
		}
		return total, fmt.Errorf("物理磁盘计数不可用: %w", lastErr)
	}
	return total, nil
}

func createIcmpFile(ipv6 bool) (syscall.Handle, error) {
	open := procIcmpCreateFile
	if ipv6 {
		open = procIcmp6CreateFile
	}
	h, _, e := open.Call()
	if h == invalidHandleValue || h == 0 {
		return 0, callError(open.Name, e)
	}
	return syscall.Handle(h), nil
}

func closeIcmpFile(h syscall.Handle) {
	procIcmpCloseHandle.Call(uintptr(h))
}

// sendIcmp sends one IPv4 echo. The reply buffer is parsed defensively instead
// of assuming a Go struct layout that must match the Windows SDK.
func sendIcmp(h syscall.Handle, destination uint32, payload []byte, timeoutMS uint32) (status uint32, rttMS uint32, err error) {
	var reply [1024]byte
	size := uint16(len(payload))
	if size == 0 {
		payload = []byte{0x43, 0x53}
		size = uint16(len(payload))
	}
	r1, _, e := procIcmpSendEcho.Call(
		uintptr(h),
		uintptr(destination),
		uintptr(unsafe.Pointer(&payload[0])),
		uintptr(size),
		0,
		uintptr(unsafe.Pointer(&reply[0])),
		uintptr(len(reply)),
		uintptr(timeoutMS),
	)
	if r1 == 0 {
		return 0, 0, callError("IcmpSendEcho", e)
	}
	status = binary.LittleEndian.Uint32(reply[4:8])
	rttMS = binary.LittleEndian.Uint32(reply[8:12])
	return status, rttMS, nil
}

// sendIcmp6 sends a synchronous IPv6 echo and reads the two DWORD fields
// following the 28-byte IPV6_ADDRESS_EX in ICMPV6_ECHO_REPLY.
func sendIcmp6(h syscall.Handle, dst syscall.RawSockaddrInet6, payload []byte, timeoutMS uint32) (uint32, uint32, error) {
	source := syscall.RawSockaddrInet6{Family: syscall.AF_INET6}
	var reply [1024]byte
	n, _, err := procIcmp6SendEcho2.Call(
		uintptr(h), 0, 0, 0,
		uintptr(unsafe.Pointer(&source)), uintptr(unsafe.Pointer(&dst)),
		uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), 0,
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeoutMS),
	)
	if n == 0 {
		return 0, 0, callError("Icmp6SendEcho2", err)
	}
	return binary.LittleEndian.Uint32(reply[28:32]), binary.LittleEndian.Uint32(reply[32:36]), nil
}

func callError(op string, e error) error {
	if e == nil {
		code, _, _ := procGetLastError.Call()
		if code == 0 {
			return fmt.Errorf("%s 失败", op)
		}
		return fmt.Errorf("%s 失败 (Win32 %d)", op, code)
	}
	if errno, ok := e.(syscall.Errno); ok && errno == 0 {
		return fmt.Errorf("%s 失败", op)
	}
	return fmt.Errorf("%s 失败: %w", op, e)
}

func isErrno(err error, want syscall.Errno) bool {
	if err == nil {
		return false
	}
	var e syscall.Errno
	if errors.As(err, &e) {
		return e == want
	}
	return false
}

func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	var e syscall.Errno
	if errors.As(err, &e) {
		return e == syscall.ERROR_ACCESS_DENIED || e == syscall.ERROR_PRIVILEGE_NOT_HELD
	}
	return false
}

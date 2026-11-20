#import "processes.h"

#import <libproc.h>
#import <sys/proc_info.h>
#import <sys/resource.h>
#import <unistd.h>
#include <string.h>

@interface DPProcess : NSObject
@property pid_t pid;
@property pid_t parentPID;
@property uint64_t born;
@property uint64_t cpuNanos;
@property uint64_t footprint;
@property BOOL countersAvailable;
@property(copy) NSString *name;
@property(strong) NSNumber *cpuRatio;
@property(copy) NSArray<DPProcess *> *children;
@end
@implementation DPProcess
@end

static NSDictionary<NSNumber *, DPProcess *> *collectProcesses(void) {
    enum { limit = 4096 };
    pid_t pids[limit];
    uid_t owner = geteuid();
    int count = proc_listpids(PROC_UID_ONLY, owner, pids, sizeof(pids)) / (int)sizeof(pid_t);
    NSMutableDictionary<NSNumber *, DPProcess *> *rows = [NSMutableDictionary dictionary];
    for (int i = 0; i < count && i < limit; ++i) {
        pid_t pid = pids[i];
        if (pid <= 0) continue;
        struct proc_bsdinfo info = {0};
        if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, sizeof(info)) != sizeof(info) ||
            info.pbi_uid != owner || info.pbi_start_tvsec == 0) {
            continue;
        }
        DPProcess *row = [[DPProcess alloc] init];
        row.pid = pid;
        row.parentPID = (pid_t)info.pbi_ppid;
        row.born = info.pbi_start_tvsec * (uint64_t)1000000 + info.pbi_start_tvusec;
        const char *name = info.pbi_name[0] ? info.pbi_name : info.pbi_comm;
        size_t size = info.pbi_name[0] ? sizeof(info.pbi_name) : sizeof(info.pbi_comm);
        NSString *decoded = [[NSString alloc] initWithBytes:name length:strnlen(name, size) encoding:NSUTF8StringEncoding];
        row.name = decoded.length > 0 ? [[decoded componentsSeparatedByCharactersInSet:NSCharacterSet.controlCharacterSet]
            componentsJoinedByString:@" "] : [NSString stringWithFormat:@"%d", pid];
        struct rusage_info_v2 usage = {0};
        if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&usage) == 0) {
            struct proc_bsdinfo after = {0};
            if (proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &after, sizeof(after)) != sizeof(after) ||
                after.pbi_uid != owner || after.pbi_start_tvsec != info.pbi_start_tvsec ||
                after.pbi_start_tvusec != info.pbi_start_tvusec) {
                continue;
            }
            row.cpuNanos = usage.ri_user_time + usage.ri_system_time;
            row.footprint = usage.ri_phys_footprint;
            row.countersAvailable = YES;
        }
        rows[@(pid)] = row;
    }
    return rows;
}

BOOL DPCheckProcessSampler(void) {
    DPProcess *selfRow = collectProcesses()[@(getpid())];
    return selfRow != nil && selfRow.countersAvailable && selfRow.footprint > 0;
}

static NSArray<DPProcess *> *sortedRows(NSArray<DPProcess *> *rows) {
    return [rows sortedArrayUsingComparator:^NSComparisonResult(DPProcess *left, DPProcess *right) {
        if (left.footprint != right.footprint) return left.footprint > right.footprint ? NSOrderedAscending : NSOrderedDescending;
        return [@(left.pid) compare:@(right.pid)];
    }];
}

static NSString *identity(DPProcess *row) {
    return [NSString stringWithFormat:@"%d/%llu", row.pid, (unsigned long long)row.born];
}

@interface DPProcessWindow () <NSOutlineViewDataSource, NSOutlineViewDelegate, NSWindowDelegate>
@property BOOL chinese;
@property BOOL busy;
@property NSUInteger generation;
@property double lastAt;
@property(strong) NSPanel *panel;
@property(strong) NSOutlineView *outline;
@property(strong) NSTextField *caption;
@property(strong) NSTimer *ticker;
@property(copy) NSDictionary<NSNumber *, DPProcess *> *previous;
@property(copy) NSArray<DPProcess *> *roots;
@property(copy) NSDictionary<NSString *, DPProcess *> *visibleRows;
@end

@implementation DPProcessWindow
- (instancetype)initWithChinese:(BOOL)chinese {
    self = [super init];
    if (self) _chinese = chinese;
    return self;
}

- (NSString *)zh:(NSString *)zh en:(NSString *)en { return self.chinese ? zh : en; }

- (void)show {
    if (!self.panel) [self createPanel];
    [NSApp activateIgnoringOtherApps:YES];
    [self.panel makeKeyAndOrderFront:nil];
    if (!self.ticker) {
        self.ticker = [NSTimer scheduledTimerWithTimeInterval:5 target:self
            selector:@selector(onClock:) userInfo:nil repeats:YES];
        self.ticker.tolerance = .5;
    }
    [self onClock:nil];
}

- (BOOL)hasVisibleProcessRows {
    return self.panel.isVisible && self.outline.numberOfRows > 0;
}

- (void)close {
    [self.panel close];
}

- (void)createPanel {
    NSRect frame = NSMakeRect(0, 0, 560, 400);
    self.panel = [[NSPanel alloc] initWithContentRect:frame styleMask:NSWindowStyleMaskTitled |
        NSWindowStyleMaskClosable | NSWindowStyleMaskResizable backing:NSBackingStoreBuffered defer:NO];
    self.panel.minSize = NSMakeSize(470, 300);
    self.panel.releasedWhenClosed = NO;
    self.panel.delegate = self;
    self.panel.title = [self zh:@"进程详情" en:@"Processes"];
    [self.panel center];
    NSView *view = self.panel.contentView;

    self.caption = [NSTextField labelWithString:[self zh:@"仅当前用户 · 正在采样" en:@"Current user · Sampling"]];
    self.caption.textColor = NSColor.secondaryLabelColor;
    self.caption.translatesAutoresizingMaskIntoConstraints = NO;
    [view addSubview:self.caption];
    NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:NSZeroRect];
    scroll.translatesAutoresizingMaskIntoConstraints = NO;
    scroll.hasVerticalScroller = YES;
    [view addSubview:scroll];
    self.outline = [[NSOutlineView alloc] initWithFrame:frame];
    self.outline.rowHeight = 28;
    self.outline.usesAlternatingRowBackgroundColors = YES;
    NSView *header = [[NSTableHeaderView alloc] initWithFrame:NSMakeRect(0, 0, 560, 26)];
    self.outline.headerView = (NSTableHeaderView *)header;
    NSTableColumn *app = [[NSTableColumn alloc] initWithIdentifier:@"app"];
    app.title = [self zh:@"进程 / 子进程" en:@"Process / child"];
    app.width = 320;
    NSTableColumn *cpu = [[NSTableColumn alloc] initWithIdentifier:@"cpu"];
    cpu.title = @"CPU";
    cpu.width = 70;
    NSTableColumn *mem = [[NSTableColumn alloc] initWithIdentifier:@"memory"];
    mem.title = [self zh:@"内存占用" en:@"Footprint"];
    mem.width = 110;
    for (NSTableColumn *column in @[app, cpu, mem]) [self.outline addTableColumn:column];
    self.outline.outlineTableColumn = app;
    self.outline.delegate = self;
    self.outline.dataSource = self;
    scroll.documentView = self.outline;
    [NSLayoutConstraint activateConstraints:@[
        [self.caption.topAnchor constraintEqualToAnchor:view.topAnchor constant:14],
        [self.caption.leadingAnchor constraintEqualToAnchor:view.leadingAnchor constant:16],
        [self.caption.trailingAnchor constraintLessThanOrEqualToAnchor:view.trailingAnchor constant:-16],
        [scroll.topAnchor constraintEqualToAnchor:self.caption.bottomAnchor constant:10],
        [scroll.leadingAnchor constraintEqualToAnchor:view.leadingAnchor],
        [scroll.trailingAnchor constraintEqualToAnchor:view.trailingAnchor],
        [scroll.bottomAnchor constraintEqualToAnchor:view.bottomAnchor],
    ]];
}

- (void)windowWillClose:(NSNotification *)notification {
    (void)notification;
    [self.ticker invalidate];
    self.ticker = nil;
    self.generation++;
    self.busy = NO;
    self.lastAt = 0;
    self.previous = nil;
    self.roots = nil;
    self.visibleRows = nil;
    [self.outline reloadData];
}

- (void)onClock:(NSTimer *)timer {
    (void)timer;
    if (self.busy || !self.ticker) return;
    self.busy = YES;
    NSUInteger generation = self.generation;
    __weak DPProcessWindow *weakSelf = self;
    dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
        NSDictionary<NSNumber *, DPProcess *> *rows = collectProcesses();
        double now = NSProcessInfo.processInfo.systemUptime;
        dispatch_async(dispatch_get_main_queue(), ^{
            DPProcessWindow *window = weakSelf;
            if (!window || window.generation != generation) return;
            window.busy = NO;
            [window updateRows:rows timestamp:now];
        });
    });
}

- (void)updateRows:(NSDictionary<NSNumber *, DPProcess *> *)rows timestamp:(double)now {
    NSMutableSet<NSString *> *expanded = [NSMutableSet set];
    for (DPProcess *row in self.visibleRows.allValues) {
        if ([self.outline isItemExpanded:row]) [expanded addObject:identity(row)];
    }
    NSMutableDictionary<NSNumber *, NSMutableArray<DPProcess *> *> *children = [NSMutableDictionary dictionary];
    NSMutableDictionary<NSString *, DPProcess *> *lookup = [NSMutableDictionary dictionary];
    NSMutableArray<DPProcess *> *roots = [NSMutableArray array];
    double interval = now - self.lastAt;
    NSUInteger cores = MAX((NSUInteger)1, NSProcessInfo.processInfo.activeProcessorCount);
    for (DPProcess *row in rows.allValues) {
        DPProcess *before = self.previous[@(row.pid)];
        if (interval >= 1 && row.countersAvailable && before.countersAvailable && before.born == row.born &&
            row.cpuNanos >= before.cpuNanos) {
            row.cpuRatio = @(MIN(1.0, (row.cpuNanos - before.cpuNanos) / (interval * 1e9 * cores)));
        }
        lookup[identity(row)] = row;
        DPProcess *parent = rows[@(row.parentPID)];
        // Start time prevents linking a reused PID or creating a cyclic tree.
        if (parent && parent.born < row.born) {
            NSNumber *key = @(parent.pid);
            if (!children[key]) children[key] = [NSMutableArray array];
            [children[key] addObject:row];
        } else {
            [roots addObject:row];
        }
    }
    for (DPProcess *row in rows.allValues) row.children = sortedRows(children[@(row.pid)]);
    self.roots = sortedRows(roots);
    self.visibleRows = lookup;
    self.previous = rows;
    self.lastAt = now;
    self.caption.stringValue = [NSString stringWithFormat:[self zh:@"仅当前用户 · %lu 个可读进程 · CPU 按整机容量" en:@"Current user · %lu readable processes · CPU as share of the computer"],
        (unsigned long)rows.count];
    [self.outline reloadData];
    // Expand top down so a saved descendant remains visible after refresh.
    for (NSInteger i = 0; i < self.outline.numberOfRows; ++i) {
        DPProcess *row = [self.outline itemAtRow:i];
        if ([expanded containsObject:identity(row)]) [self.outline expandItem:row];
    }
}

- (NSArray<DPProcess *> *)childrenOf:(DPProcess *)item { return item ? item.children : self.roots; }

- (NSInteger)outlineView:(NSOutlineView *)outlineView numberOfChildrenOfItem:(id)item {
    (void)outlineView;
    return (NSInteger)[self childrenOf:item].count;
}

- (id)outlineView:(NSOutlineView *)outlineView child:(NSInteger)index ofItem:(id)item {
    (void)outlineView;
    return [self childrenOf:item][(NSUInteger)index];
}

- (BOOL)outlineView:(NSOutlineView *)outlineView isItemExpandable:(id)item {
    (void)outlineView;
    return [(DPProcess *)item children].count > 0;
}

- (NSView *)outlineView:(NSOutlineView *)outlineView viewForTableColumn:(NSTableColumn *)column item:(id)item {
    (void)outlineView;
    DPProcess *row = item;
    NSString *text = @"—";
    if ([column.identifier isEqualToString:@"app"]) {
        text = row.children.count ? [NSString stringWithFormat:@"%@ · %lu", row.name, (unsigned long)row.children.count] : row.name;
    } else if ([column.identifier isEqualToString:@"cpu"] && row.cpuRatio) {
        text = [NSString stringWithFormat:@"%.1f%%", 100 * row.cpuRatio.doubleValue];
    } else if ([column.identifier isEqualToString:@"memory"] && row.countersAvailable) {
        text = [NSByteCountFormatter stringFromByteCount:(long long)row.footprint countStyle:NSByteCountFormatterCountStyleMemory];
    }
    NSTextField *cell = [NSTextField labelWithString:text];
    cell.lineBreakMode = NSLineBreakByTruncatingTail;
    cell.font = [NSFont monospacedDigitSystemFontOfSize:12 weight:NSFontWeightRegular];
    return cell;
}
@end

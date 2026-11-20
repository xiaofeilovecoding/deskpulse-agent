#import <AppKit/AppKit.h>
#import <IOKit/IOKitLib.h>
#import <mach/mach.h>
#import <sys/sysctl.h>
#import <time.h>
#include <stdio.h>
#include <string.h>
#import "processes.h"
#import "latency.h"
#import "model.h"

typedef struct {
    uint64_t totalTicks, idleTicks, physicalBytes, availableBytes, readBytes, writeBytes;
    BOOL cpuValid, memoryValid, diskValid;
} MetricSample;

static MetricSample collect(BOOL includeDisk) {
    MetricSample sample = {0};
    mach_port_t host = mach_host_self();
    host_cpu_load_info_data_t load = {0};
    mach_msg_type_number_t cpuCount = HOST_CPU_LOAD_INFO_COUNT;
    if (host_statistics(host, HOST_CPU_LOAD_INFO, (host_info_t)&load, &cpuCount) == KERN_SUCCESS) {
        for (NSUInteger state = 0; state < CPU_STATE_MAX; ++state) {
            sample.totalTicks += load.cpu_ticks[state];
        }
        sample.idleTicks = load.cpu_ticks[CPU_STATE_IDLE];
        sample.cpuValid = YES;
    }
    vm_statistics64_data_t vm = {0};
    mach_msg_type_number_t vmCount = HOST_VM_INFO64_COUNT;
    vm_size_t bytesPerPage = 0;
    size_t size = sizeof(sample.physicalBytes);
    if (host_statistics64(host, HOST_VM_INFO64, (host_info64_t)&vm, &vmCount) == KERN_SUCCESS &&
        host_page_size(host, &bytesPerPage) == KERN_SUCCESS &&
        sysctlbyname("hw.memsize", &sample.physicalBytes, &size, NULL, 0) == 0 &&
        sample.physicalBytes > 0) {
        uint64_t reclaimablePages = (uint64_t)vm.free_count + vm.inactive_count;
        sample.availableBytes = MIN(sample.physicalBytes, reclaimablePages * bytesPerPage);
        sample.memoryValid = YES;
    }
    mach_port_deallocate(mach_task_self(), host);

    if (!includeDisk) {
        return sample;
    }
    io_iterator_t iterator = IO_OBJECT_NULL;
    CFMutableDictionaryRef matching = IOServiceMatching("IOBlockStorageDriver");
    if (matching == NULL ||
        IOServiceGetMatchingServices(kIOMainPortDefault, matching, &iterator) != KERN_SUCCESS) {
        return sample;
    }
    io_object_t driver;
    while ((driver = IOIteratorNext(iterator)) != IO_OBJECT_NULL) {
        CFMutableDictionaryRef raw = NULL;
        kern_return_t result = IORegistryEntryCreateCFProperties(driver, &raw, kCFAllocatorDefault, 0);
        IOObjectRelease(driver);
        NSDictionary *properties = CFBridgingRelease(raw);
        if (result != KERN_SUCCESS) {
            continue;
        }
        NSDictionary *stats = properties[@"Statistics"];
        if (![stats isKindOfClass:[NSDictionary class]]) {
            continue;
        }
        NSNumber *read = stats[@"Bytes (Read)"];
        NSNumber *write = stats[@"Bytes (Write)"];
        if (![read isKindOfClass:[NSNumber class]] || ![write isKindOfClass:[NSNumber class]]) {
            continue;
        }
        sample.readBytes += read.unsignedLongLongValue;
        sample.writeBytes += write.unsignedLongLongValue;
        sample.diskValid = YES;
    }
    IOObjectRelease(iterator);
    return sample;
}

static NSNumber *cpuRatio(MetricSample before, MetricSample now) {
    if (!before.cpuValid || !now.cpuValid || now.totalTicks <= before.totalTicks ||
        now.idleTicks < before.idleTicks) {
        return nil;
    }
    uint64_t elapsed = now.totalTicks - before.totalTicks;
    uint64_t idle = now.idleTicks - before.idleTicks;
    return idle <= elapsed ? @((double)(elapsed - idle) / elapsed) : nil;
}

static NSString *rate(uint64_t current, uint64_t previous, double seconds) {
    if (current < previous || seconds <= 0) {
        return @"—";
    }
    double amount = (current - previous) / seconds;
    return amount < 1024 * 1024 ? [NSString stringWithFormat:@"%.0f KiB/s", amount / 1024]
        : [NSString stringWithFormat:@"%.1f MiB/s", amount / (1024 * 1024)];
}

static NSDictionary *exportMetric(NSString *name, NSString *unit, NSNumber *value) {
    return @{@"metric_id": name, @"value": value ?: NSNull.null, @"unit": unit,
        @"quality": value ? @"ok" : @"unknown"};
}

static NSArray<NSDictionary *> *snapshotMetrics(NSNumber *cpu, MetricSample sample, NSNumber *read, NSNumber *write) {
    return @[exportMetric(@"cpu.used_ratio", @"ratio", cpu),
        exportMetric(@"memory.available_bytes", @"bytes", sample.memoryValid ? @(sample.availableBytes) : nil),
        exportMetric(@"memory.total_bytes", @"bytes", sample.memoryValid ? @(sample.physicalBytes) : nil),
        exportMetric(@"disk.read_bytes_per_sec", @"bytes_per_sec", read),
        exportMetric(@"disk.write_bytes_per_sec", @"bytes_per_sec", write)];
}

static NSData *encodeExport(NSArray<NSDictionary *> *metrics, NSDate *date) {
    NSISO8601DateFormatter *formatter = [[NSISO8601DateFormatter alloc] init];
    return [NSJSONSerialization dataWithJSONObject:@{@"schema_version": @1, @"device_alias": @"local-export",
        @"captured_at": [formatter stringFromDate:date], @"scope": @{@"kind": @"user_device", @"scope_id": @"local"},
        @"window_seconds": @5, @"metrics": metrics} options:NSJSONWritingPrettyPrinted error:NULL];
}

@interface DeskPulse : NSObject <NSApplicationDelegate>
@property(strong) NSStatusItem *status;
@property(strong) NSPopover *popover;
@property(strong) NSTextField *cpuLabel;
@property(strong) NSTextField *memoryLabel;
@property(strong) NSTextField *diskLabel;
@property(strong) NSTextField *diagnosisLabel;
@property(strong) NSTextField *networkLabel;
@property(strong) NSTimer *ticker;
@property(assign) MetricSample previous;
@property(assign) double previousAt;
@property(assign) BOOL chinese;
@property(strong) NSMutableArray<NSNumber *> *cpuHistory;
@property(strong) DPProcessWindow *processWindow;
@property(copy) NSArray<NSString *> *targets;
@property(strong) NSMutableDictionary<NSString *, id> *latency;
@property double lastProbe;
@property NSUInteger probeGeneration;
@property(strong) DPModelPanel *model;
@property(strong) NSMutableArray<NSDictionary *> *diagnosticSamples;
@property(copy) NSArray<NSDictionary *> *lastExportMetrics;
@property(strong) NSDate *lastExportDate;
@property(strong) NSButton *appearanceButton;
@property BOOL dark;
@property(copy) NSString *uiCheckDirectory;
@property BOOL uiCheckChinese;
- (BOOL)checkMenuUI;
@end

@implementation DeskPulse

- (NSString *)zh:(NSString *)chinese en:(NSString *)english {
    return self.chinese ? chinese : english;
}

- (NSTextField *)row:(NSString *)title in:(NSStackView *)stack {
    NSTextField *field = [NSTextField wrappingLabelWithString:title];
    field.font = [NSFont monospacedDigitSystemFontOfSize:13 weight:NSFontWeightMedium];
    [field.widthAnchor constraintEqualToConstant:294].active = YES;
    [stack addArrangedSubview:field];
    return field;
}

- (void)applicationDidFinishLaunching:(NSNotification *)note {
    (void)note;
    BOOL checkingUI = self.uiCheckDirectory != nil;
    self.chinese = checkingUI ? self.uiCheckChinese : [[NSLocale preferredLanguages].firstObject hasPrefix:@"zh"];
    self.dark = !checkingUI && [NSUserDefaults.standardUserDefaults boolForKey:@"darkAppearance"];
    [self applyAppearance];
    self.cpuHistory = [NSMutableArray array];
    self.model = [[DPModelPanel alloc] initWithChinese:self.chinese];
    self.diagnosticSamples = [NSMutableArray array];
    NSString *configuredTargets = checkingUI ? @"" : ([NSUserDefaults.standardUserDefaults stringForKey:@"networkTargets"] ?: @"");
    self.targets = DPNormalizeTargets(configuredTargets, NULL) ?: @[];
    self.latency = [NSMutableDictionary dictionary];
    self.status = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    self.status.button.image = [NSImage imageWithSystemSymbolName:@"waveform.path"
        accessibilityDescription:@"DeskPulse Agent"];
    self.status.button.toolTip = @"DeskPulse Agent";
    self.status.button.target = self;
    self.status.button.action = @selector(toggle:);
    self.popover = [[NSPopover alloc] init];
    self.popover.behavior = NSPopoverBehaviorTransient;
    self.popover.contentSize = NSMakeSize(330, 344);
    NSViewController *panel = [[NSViewController alloc] init];
    panel.view = [[NSView alloc] initWithFrame:NSMakeRect(0,0,330,344)];
    self.popover.contentViewController = panel;

    NSStackView *stack = [[NSStackView alloc] initWithFrame:NSZeroRect];
    stack.orientation = NSUserInterfaceLayoutOrientationVertical;
    stack.alignment = NSLayoutAttributeLeading;
    stack.spacing = 10;
    stack.translatesAutoresizingMaskIntoConstraints = NO;
    [panel.view addSubview:stack];
    [NSLayoutConstraint activateConstraints:@[
        [stack.leadingAnchor constraintEqualToAnchor:panel.view.leadingAnchor constant:18],
        [stack.topAnchor constraintEqualToAnchor:panel.view.topAnchor constant:17],
        [stack.trailingAnchor constraintLessThanOrEqualToAnchor:panel.view.trailingAnchor constant:-16]
    ]];
    NSTextField *title = [NSTextField labelWithString:@"DeskPulse Agent"];
    title.font = [NSFont systemFontOfSize:17 weight:NSFontWeightSemibold];
    self.appearanceButton = [NSButton buttonWithTitle:@"" target:self action:@selector(toggleAppearance:)];
    self.appearanceButton.imagePosition = NSImageOnly;
    self.appearanceButton.controlSize = NSControlSizeSmall;
    [self.appearanceButton.widthAnchor constraintEqualToConstant:30].active = YES;
    [self.appearanceButton.heightAnchor constraintEqualToConstant:26].active = YES;
    [self applyAppearance];
    NSStackView *header = [NSStackView stackViewWithViews:@[title, self.appearanceButton]];
    header.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    header.spacing = 12;
    [title setContentHuggingPriority:NSLayoutPriorityDefaultLow forOrientation:NSLayoutConstraintOrientationHorizontal];
    [header.widthAnchor constraintEqualToConstant:294].active = YES;
    [stack addArrangedSubview:header];
    self.cpuLabel = [self row:@"CPU · —" in:stack];
    self.memoryLabel = [self row:[self zh:@"内存 · —" en:@"Memory · —"] in:stack];
    self.diskLabel = [self row:@"I/O · —" in:stack];
    self.networkLabel = [self row:@"" in:stack];
    self.networkLabel.textColor = NSColor.secondaryLabelColor;
    self.networkLabel.maximumNumberOfLines = 2;
    self.diagnosisLabel = [self row:@"" in:stack];
    self.diagnosisLabel.textColor = NSColor.secondaryLabelColor;
    self.diagnosisLabel.maximumNumberOfLines = 2;
    NSButton *diagnose = [NSButton buttonWithTitle:[self zh:@"AI 诊断" en:@"AI diagnosis"]
        target:self action:@selector(runDiagnosis:)];
    diagnose.bezelStyle = NSBezelStyleRounded;
    NSButton *models = [NSButton buttonWithTitle:[self zh:@"模型设置" en:@"Model settings"]
        target:self action:@selector(configureModel:)];
    NSStackView *actionRow = [NSStackView stackViewWithViews:@[diagnose, models]];
    actionRow.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    actionRow.spacing = 8;
    [stack addArrangedSubview:actionRow];
    NSButton *processes = [NSButton buttonWithTitle:[self zh:@"查看进程" en:@"View processes"]
        target:self action:@selector(showProcesses:)];
    NSButton *network = [NSButton buttonWithTitle:[self zh:@"网络目标" en:@"Network targets"]
        target:self action:@selector(configureNetwork:)];
    NSButton *export = [NSButton buttonWithTitle:[self zh:@"导出" en:@"Export"]
        target:self action:@selector(exportSnapshot:)];
    for (NSButton *button in @[processes, network, export]) button.controlSize = NSControlSizeSmall;
    NSStackView *detailsRow = [NSStackView stackViewWithViews:@[processes, network, export]];
    detailsRow.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    detailsRow.spacing = 8;
    [stack addArrangedSubview:detailsRow];
    NSButton *quit = [NSButton buttonWithTitle:[self zh:@"退出" en:@"Quit"]
        target:NSApp action:@selector(terminate:)];
    [stack addArrangedSubview:quit];
    if (checkingUI) {
        self.cpuLabel.stringValue = @"CPU · 24%";
        self.memoryLabel.stringValue = [self zh:@"可用内存 · 6.8 GiB" en:@"Available memory · 6.8 GiB"];
        self.diskLabel.stringValue = @"I/O · ↓ 2.4 MB/s  ↑ 0.8 MB/s";
        self.networkLabel.stringValue = [self zh:@"延迟 · 未配置" en:@"Latency · No targets configured"];
        self.diagnosisLabel.stringValue = [self zh:@"持续卡顿时，可运行 AI 诊断。" en:@"If a slowdown persists, run AI diagnosis."];
        dispatch_async(dispatch_get_main_queue(), ^{
            exit([self checkMenuUI] ? 0 : 1);
        });
        return;
    }
    [self update];
    self.ticker = [NSTimer scheduledTimerWithTimeInterval:5 target:self
        selector:@selector(onClock:) userInfo:nil repeats:YES];
    self.ticker.tolerance = .5;
}

- (NSString *)appearanceAction {
    return self.dark ? [self zh:@"切换到浅色模式" en:@"Switch to light mode"]
                     : [self zh:@"切换到暗色模式" en:@"Switch to dark mode"];
}

- (void)applyAppearance {
    NSApp.appearance = [NSAppearance appearanceNamed:self.dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
    NSString *label = [self appearanceAction];
    NSString *symbol = self.dark ? @"sun.max" : @"moon";
    self.appearanceButton.image = [NSImage imageWithSystemSymbolName:symbol accessibilityDescription:label];
    self.appearanceButton.toolTip = label;
    self.appearanceButton.accessibilityLabel = label;
}

- (void)toggleAppearance:(id)sender {
    (void)sender;
    self.dark = !self.dark;
    if (!self.uiCheckDirectory) [NSUserDefaults.standardUserDefaults setBool:self.dark forKey:@"darkAppearance"];
    [self applyAppearance];
}

- (void)exportSnapshot:(id)sender {
    (void)sender;
    [self.popover close];
    [NSApp activateIgnoringOtherApps:YES];
    if (!self.lastExportDate) return;
    NSMutableArray<NSDictionary *> *metrics = [self.lastExportMetrics mutableCopy];
    if (self.lastProbe && NSProcessInfo.processInfo.systemUptime-self.lastProbe < 5) {
        NSUInteger index = 0;
        for (NSString *target in self.targets) {
            id sample = self.latency[target];
            ++index;
            if (![sample isKindOfClass:NSNumber.class]) continue;
            NSMutableDictionary *metric = [exportMetric(@"network.tcp_connect_ms", @"ms", sample) mutableCopy];
            metric[@"target_alias"] = [NSString stringWithFormat:@"R%lu", (unsigned long)index];
            metric[@"protocol"] = @"tcp";
            [metrics addObject:metric];
        }
    }
    NSData *data = encodeExport(metrics, self.lastExportDate);
    NSSavePanel *panel = [NSSavePanel savePanel];
    panel.nameFieldStringValue = @"metrics-v1.json";
    panel.message = [self zh:@"导出本机汇总指标。进程名、网络地址和凭据不会写入文件。"
        en:@"Export aggregate measurements. Process names, network addresses and credentials are excluded."];
    if (data && [panel runModal] == NSModalResponseOK &&
        ![data writeToURL:panel.URL options:NSDataWritingAtomic error:NULL]) {
        NSAlert *alert = [[NSAlert alloc] init];
        alert.messageText = [self zh:@"无法写入指标文件" en:@"Could not save metrics"];
        [alert runModal];
    }
}

- (void)configureModel:(id)sender {
    (void)sender;
    [self.popover close];
    [NSApp activateIgnoringOtherApps:YES];
    [self.model configure];
}

- (void)runDiagnosis:(id)sender {
    (void)sender;
    [self.popover close];
    [NSApp activateIgnoringOtherApps:YES];
    NSMutableArray<NSDictionary *> *samples = [NSMutableArray array];
    double after = NSProcessInfo.processInfo.systemUptime - 30;
    for (NSDictionary *entry in self.diagnosticSamples) {
        if ([entry[@"at"] doubleValue] >= after) [samples addObject:entry[@"values"]];
    }
    [self.model diagnose:[samples copy]];
}

- (void)configureNetwork:(id)sender {
    (void)sender;
    [self.popover close];
    [NSApp activateIgnoringOtherApps:YES];
    NSAlert *dialog = [[NSAlert alloc] init];
    dialog.messageText = [self zh:@"网络测点" en:@"Network targets"];
    dialog.informativeText = [self zh:@"每行填写一个主机名或 host:port，最多 8 个。例如 www.baidu.com:443。不填则关闭。仅在菜单栏面板展开时测试 TCP 建连。"
        en:@"Enter up to eight hosts or host:port pairs, one per line. Leave empty to disable. TCP connections are measured only while the menu panel is open."];
    [dialog addButtonWithTitle:[self zh:@"保存" en:@"Save"]];
    [dialog addButtonWithTitle:[self zh:@"取消" en:@"Cancel"]];
    NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:NSMakeRect(0, 0, 320, 125)];
    scroll.hasVerticalScroller = YES;
    NSTextView *field = [[NSTextView alloc] initWithFrame:scroll.bounds];
    field.font = [NSFont monospacedSystemFontOfSize:13 weight:NSFontWeightRegular];
    field.verticallyResizable = YES;
    field.automaticQuoteSubstitutionEnabled = NO;
    field.automaticDashSubstitutionEnabled = NO;
    field.string = [self.targets componentsJoinedByString:@"\n"];
    scroll.documentView = field;
    dialog.accessoryView = scroll;
    if ([dialog runModal] != NSAlertFirstButtonReturn) return;
    NSArray<NSString *> *targets = DPNormalizeTargets(field.string, NULL);
    if (!targets) {
        NSAlert *failure = [[NSAlert alloc] init];
        failure.messageText = [self zh:@"无法保存网络目标" en:@"Could not save network targets"];
        failure.informativeText = [self zh:@"请输入最多 8 个有效的主机名或 host:port。"
            en:@"Enter up to eight valid hosts or host:port pairs."];
        [failure runModal];
        return;
    }
    self.targets = targets;
    [self.latency removeAllObjects];
    self.probeGeneration++;
    self.lastProbe = 0;
    [NSUserDefaults.standardUserDefaults setObject:[targets componentsJoinedByString:@"\n"] forKey:@"networkTargets"];
}

- (void)updateLatency {
    if (!self.targets.count) {
        self.networkLabel.stringValue = [self zh:@"网络 · 探测关闭" en:@"Network · Probing off"];
        return;
    }
    double now = NSProcessInfo.processInfo.systemUptime;
    if (self.lastProbe == 0 || now - self.lastProbe >= 30) {
        self.lastProbe = now;
        NSUInteger generation = ++self.probeGeneration;
        __weak DeskPulse *weakSelf = self;
        for (NSString *target in self.targets) {
            DPProbeLatency(target, dispatch_get_main_queue(), ^(NSNumber *milliseconds) {
                DeskPulse *app = weakSelf;
                if (!app || app.probeGeneration != generation) return;
                app.latency[target] = milliseconds ?: NSNull.null;
                if (app.popover.shown) [app showLatency];
            });
        }
    }
    [self showLatency];
}

- (void)showLatency {
    NSString *target = self.targets.firstObject;
    id sample = target ? self.latency[target] : nil;
    NSString *status = [self zh:@"待测" en:@"pending"];
    if (sample == NSNull.null) status = [self zh:@"连接失败或超时" en:@"unreachable or timed out"];
    if ([sample isKindOfClass:NSNumber.class]) status = [NSString stringWithFormat:@"%.0f ms", [sample doubleValue]];
    NSUInteger secondary = self.targets.count ? self.targets.count-1 : 0;
    NSString *suffix = secondary ? [NSString stringWithFormat:@" +%lu", (unsigned long)secondary] : @"";
    self.networkLabel.stringValue = [NSString stringWithFormat:@"TCP · %@ · %@%@", target ?: @"—", status, suffix];
    self.networkLabel.toolTip = [self latencyDetails];
}

- (NSString *)latencyDetails {
    NSMutableArray<NSString *> *details = [NSMutableArray array];
    for (NSString *target in self.targets) {
        id sample = self.latency[target];
        NSString *value = [self zh:@"待测" en:@"pending"];
        if (sample == NSNull.null) value = [self zh:@"连接失败或超时" en:@"unreachable or timed out"];
        if ([sample isKindOfClass:NSNumber.class]) value = [NSString stringWithFormat:@"%.0f ms", [sample doubleValue]];
        [details addObject:[NSString stringWithFormat:@"%@ · %@", target, value]];
    }
    return [details componentsJoinedByString:@"\n"];
}

- (void)showProcesses:(id)sender {
    (void)sender;
    [self.popover close];
    if (!self.processWindow) self.processWindow = [[DPProcessWindow alloc] initWithChinese:self.chinese];
    [self.processWindow show];
}

- (void)onClock:(NSTimer *)timer {
    (void)timer;
    [self update];
}

- (void)toggle:(id)sender {
    (void)sender;
    if (self.popover.shown) {
        [self.popover close];
    } else {
        self.popover.animates = !self.uiCheckDirectory && !NSWorkspace.sharedWorkspace.accessibilityDisplayShouldReduceMotion;
        [self.popover showRelativeToRect:self.status.button.bounds
            ofView:self.status.button preferredEdge:NSRectEdgeMinY];
        if (!self.uiCheckDirectory) [self update];
    }
}

// Test the same status item and AppKit view hierarchy used during normal runs.
- (BOOL)capturePopover:(NSString *)name {
    NSView *popoverView = self.popover.contentViewController.view;
    NSView *surface = popoverView.window.contentView ?: popoverView;
    [surface layoutSubtreeIfNeeded];
    [surface displayIfNeeded];
    NSRect bounds = surface.bounds;
    NSBitmapImageRep *bitmap = [surface bitmapImageRepForCachingDisplayInRect:bounds];
    if (!bitmap || bitmap.pixelsWide < 300 || bitmap.pixelsHigh < 300) return NO;
    [surface cacheDisplayInRect:bounds toBitmapImageRep:bitmap];
    NSData *png = [bitmap representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
    return [png writeToFile:[self.uiCheckDirectory stringByAppendingPathComponent:name] atomically:YES];
}

- (BOOL)checkMenuUI {
    if (!self.status.button.window || !self.status.button.image) {
        fputs("macOS status item is missing its window or image\n", stderr);
        return NO;
    }
    [self.status.button performClick:nil];
    if (!self.popover.shown || self.dark || !self.appearanceButton.image ||
        ![self.appearanceButton.accessibilityLabel isEqualToString:[self appearanceAction]] ||
        ![self capturePopover:@"light.png"]) {
        fputs("macOS light popover check failed\n", stderr);
        return NO;
    }
    [self.appearanceButton performClick:nil];
    if (!self.dark || ![NSApp.appearance.name isEqualToString:NSAppearanceNameDarkAqua] ||
        ![self.appearanceButton.toolTip isEqualToString:[self appearanceAction]] ||
        ![self capturePopover:@"dark.png"]) {
        fputs("macOS dark popover check failed\n", stderr);
        return NO;
    }
    [self.appearanceButton performClick:nil];
    if (self.dark || ![NSApp.appearance.name isEqualToString:NSAppearanceNameAqua]) {
        fputs("macOS popover could not switch back to light\n", stderr);
        return NO;
    }
    [self.status.button performClick:nil];
    if (self.popover.shown) {
        fputs("macOS status item could not close the popover\n", stderr);
        return NO;
    }
    puts("macOS status item, native popover and both theme actions passed");
    return YES;
}

- (void)update {
    const BOOL visible = self.popover.shown;
    MetricSample before = self.previous;
    MetricSample now = collect(visible);
    const double elapsed = NSProcessInfo.processInfo.systemUptime - self.previousAt;
    NSNumber *cpu = elapsed >= 1 ? cpuRatio(before, now) : nil;
    if (elapsed >= 4) {
        NSMutableDictionary *values = [NSMutableDictionary dictionaryWithCapacity:2];
        if (cpu) values[@"cpu_percent"] = @(100 * cpu.doubleValue);
        if (now.memoryValid) values[@"estimated_memory_percent"] =
            @(100 * ((double)(now.physicalBytes-now.availableBytes) / now.physicalBytes));
        if (values.count) {
            [self.diagnosticSamples addObject:@{@"at": @(NSProcessInfo.processInfo.systemUptime), @"values": [values copy]}];
            if (self.diagnosticSamples.count > 8) [self.diagnosticSamples removeObjectAtIndex:0];
        }
    }
    if (cpu) {
        [self.cpuHistory addObject:cpu];
        while (self.cpuHistory.count > 3) {
            [self.cpuHistory removeObjectAtIndex:0];
        }
    }
    BOOL pressure = self.cpuHistory.count == 3;
    for (NSNumber *value in self.cpuHistory) {
        pressure = pressure && value.doubleValue >= .9;
    }
    self.status.button.title = pressure ? @"•" : @"";
    if (visible) {
        [self updateLatency];
        if (cpu) {
            self.cpuLabel.stringValue = [NSString stringWithFormat:@"CPU · %.0f%%", cpu.doubleValue * 100];
        }
        if (now.memoryValid) {
            NSString *label = [self zh:@"物理内存约可回收" en:@"Approx. available memory"];
            self.memoryLabel.stringValue = [NSString stringWithFormat:@"%@ · %.1f GiB", label,
                now.availableBytes / (1024.0 * 1024.0 * 1024.0)];
        }
        if (now.diskValid && before.diskValid && elapsed >= 1) {
            self.diskLabel.stringValue = [NSString stringWithFormat:@"I/O · %@  ↑%@",
                rate(now.readBytes, before.readBytes, elapsed), rate(now.writeBytes, before.writeBytes, elapsed)];
        } else if (!now.diskValid) {
            self.diskLabel.stringValue = [self zh:@"I/O · 当前设备无法读取" en:@"I/O · Unavailable on this device"];
        } else {
            self.diskLabel.stringValue = [self zh:@"I/O · 正在建立基线" en:@"I/O · Waiting for a baseline"];
        }
        self.diagnosisLabel.stringValue = pressure ?
            [self zh:@"CPU 可能较忙。连续三次样本均偏高，请重新观察。" en:@"CPU may be busy. Three consecutive samples were high. Check again shortly."] :
            [self zh:@"当前 CPU 证据不足，异常持续时再检查。" en:@"There is not enough CPU evidence yet. Check again if the slowdown persists."];
    }
    if (elapsed >= 1 || self.previousAt == 0 || (visible && !before.diskValid)) {
        NSNumber *read = nil, *write = nil;
        if (now.diskValid && before.diskValid && elapsed >= 1) {
            if (now.readBytes >= before.readBytes) read = @((now.readBytes-before.readBytes)/elapsed);
            if (now.writeBytes >= before.writeBytes) write = @((now.writeBytes-before.writeBytes)/elapsed);
        }
        self.lastExportMetrics = snapshotMetrics(cpu, now, read, write);
        self.lastExportDate = [NSDate date];
        self.previous = now;
        self.previousAt = NSProcessInfo.processInfo.systemUptime;
    }
}
@end

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc == 2 && strcmp(argv[1], "--check-process-sampler") == 0) {
            return DPCheckProcessSampler() ? 0 : 1;
        }
        if (argc == 2 && strcmp(argv[1], "--check-latency") == 0) {
            return DPCheckLatency() ? 0 : 1;
        }
        if (argc == 2 && strcmp(argv[1], "--check-export") == 0) {
            MetricSample sample = { .memoryValid = YES, .physicalBytes = 8ull << 30, .availableBytes = 2ull << 30 };
            NSData *data = encodeExport(snapshotMetrics(@(0.3), sample, nil, nil), [NSDate dateWithTimeIntervalSince1970:0]);
            if (!data) return 1;
            fwrite(data.bytes, 1, data.length, stdout);
            fputc('\n', stdout);
            return 0;
        }
        if (argc == 2 && strcmp(argv[1], "--sample") == 0) {
            MetricSample first = collect(YES);
            [NSThread sleepForTimeInterval:1];
            MetricSample second = collect(YES);
            NSNumber *cpu = cpuRatio(first, second);
            if (!cpu || !second.memoryValid) {
                fputs("macOS CPU or memory probe failed\n",stderr);
                return 1;
            }
            NSDictionary *sample = @{
                @"cpu_used_ratio": cpu,
                @"physical_memory_bytes": @(second.physicalBytes),
                @"estimated_available_memory_bytes": @(second.availableBytes),
                @"disk_counters_available": @(second.diskValid)
            };
            NSData *json = [NSJSONSerialization dataWithJSONObject:sample options:0 error:NULL];
            if (!json) return 1;
            fwrite(json.bytes, 1, json.length, stdout);
            fputc('\n',stdout);
            return 0;
        }
        NSApplication *app = NSApplication.sharedApplication;
        [app setActivationPolicy:NSApplicationActivationPolicyAccessory];
        DeskPulse *delegate = [[DeskPulse alloc] init];
        if (argc == 4 && strcmp(argv[1], "--check-ui") == 0) {
            delegate.uiCheckDirectory = [NSString stringWithUTF8String:argv[2]];
            delegate.uiCheckChinese = strcmp(argv[3], "zh-CN") == 0;
            if (!delegate.uiCheckDirectory.length) return 1;
            NSError *error = nil;
            if (![NSFileManager.defaultManager createDirectoryAtPath:delegate.uiCheckDirectory
                    withIntermediateDirectories:YES attributes:nil error:&error]) {
                fprintf(stderr, "Could not create UI evidence directory: %s\n", error.localizedDescription.UTF8String);
                return 1;
            }
        }
        app.delegate = delegate;
        [app run];
        (void)delegate;
    }
    return 0;
}

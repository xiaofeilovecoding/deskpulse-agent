#import <AppKit/AppKit.h>
#import <IOKit/IOKitLib.h>
#import <mach/mach.h>
#import <sys/sysctl.h>
#import <time.h>
#include <stdio.h>
#include <string.h>

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

@interface DeskPulse : NSObject <NSApplicationDelegate>
@property(strong) NSStatusItem *status;
@property(strong) NSPopover *popover;
@property(strong) NSTextField *cpuLabel;
@property(strong) NSTextField *memoryLabel;
@property(strong) NSTextField *diskLabel;
@property(strong) NSTextField *diagnosisLabel;
@property(strong) NSTimer *ticker;
@property(assign) MetricSample previous;
@property(assign) double previousAt;
@property(assign) BOOL chinese;
@property(strong) NSMutableArray<NSNumber *> *cpuHistory;
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
    self.chinese = [[NSLocale preferredLanguages].firstObject hasPrefix:@"zh"];
    self.cpuHistory = [NSMutableArray array];
    self.status = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
    self.status.button.image = [NSImage imageWithSystemSymbolName:@"waveform.path"
        accessibilityDescription:@"DeskPulse Agent"];
    self.status.button.toolTip = @"DeskPulse Agent";
    self.status.button.target = self;
    self.status.button.action = @selector(toggle:);
    self.popover = [[NSPopover alloc] init];
    self.popover.behavior = NSPopoverBehaviorTransient;
    self.popover.contentSize = NSMakeSize(330, 258);
    NSViewController *panel = [[NSViewController alloc] init];
    panel.view = [[NSView alloc] initWithFrame:NSMakeRect(0,0,330,258)];
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
    NSTextField *title = [self row:@"DeskPulse Agent" in:stack];
    title.font = [NSFont systemFontOfSize:17 weight:NSFontWeightSemibold];
    self.cpuLabel = [self row:@"CPU · —" in:stack];
    self.memoryLabel = [self row:[self zh:@"内存 · —" en:@"Memory · —"] in:stack];
    self.diskLabel = [self row:@"I/O · —" in:stack];
    self.diagnosisLabel = [self row:@"" in:stack];
    self.diagnosisLabel.textColor = NSColor.secondaryLabelColor;
    NSButton *quit = [NSButton buttonWithTitle:[self zh:@"退出" en:@"Quit"]
        target:NSApp action:@selector(terminate:)];
    [stack addArrangedSubview:quit];
    [self update];
    self.ticker = [NSTimer scheduledTimerWithTimeInterval:5 target:self
        selector:@selector(onClock:) userInfo:nil repeats:YES];
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
        self.popover.animates = !NSWorkspace.sharedWorkspace.accessibilityDisplayShouldReduceMotion;
        [self.popover showRelativeToRect:self.status.button.bounds
            ofView:self.status.button preferredEdge:NSRectEdgeMinY];
        [self update];
    }
}

- (void)update {
    const BOOL visible = self.popover.shown;
    MetricSample before = self.previous;
    MetricSample now = collect(visible);
    const double elapsed = NSProcessInfo.processInfo.systemUptime - self.previousAt;
    NSNumber *cpu = elapsed >= 1 ? cpuRatio(before, now) : nil;
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
        self.previous = now;
        self.previousAt = NSProcessInfo.processInfo.systemUptime;
    }
}
@end

int main(int argc, const char *argv[]) {
    @autoreleasepool {
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
        app.delegate = delegate;
        [app run];
        (void)delegate;
    }
    return 0;
}

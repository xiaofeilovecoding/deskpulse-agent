#import "model.h"
#import <Security/Security.h>

static NSArray<NSString *> *protocols(void) {
    return @[@"deepseek", @"chat_completions", @"responses", @"messages"];
}

static NSArray<NSString *> *endpoints(void) {
    return @[@"https://api.deepseek.com", @"https://api.openai.com/v1",
        @"https://api.openai.com/v1", @"https://api.anthropic.com/v1"];
}

static NSDictionary *modelProfile(NSInteger index, NSString *base, NSString *model) {
    if (index < 0 || (NSUInteger)index >= protocols().count) return nil;
    NSString *protocol = protocols()[(NSUInteger)index];
    if (!base.length) base = endpoints()[(NSUInteger)index];
    if (!model.length && index == 0) model = @"deepseek-flash";
    NSURLComponents *url = [NSURLComponents componentsWithString:base];
    if (base.length > 512 || ![url.scheme isEqualToString:@"https"] || !url.host.length ||
        url.user || url.password || url.query || url.fragment ||
        [base rangeOfCharacterFromSet:[NSCharacterSet characterSetWithCharactersInString:@"?#%\\"]].location != NSNotFound ||
        [base rangeOfCharacterFromSet:NSCharacterSet.whitespaceAndNewlineCharacterSet].location != NSNotFound ||
        ![model length] ||
        [model rangeOfString:@"\\A[A-Za-z0-9._/:-]{1,128}\\z" options:NSRegularExpressionSearch].location == NSNotFound) {
        return nil;
    }
    url.host = url.host.lowercaseString;
    NSString *path = url.path ?: @"";
    while ([path hasSuffix:@"/"]) path = [path substringToIndex:path.length-1];
    if (path.length && ![path isEqualToString:path.stringByStandardizingPath]) return nil;
    url.path = path;
    NSString *normalized = url.URL.absoluteString;
    if (!normalized.length) return nil;
    return @{@"provider": protocol, @"base_url": normalized, @"model": model};
}

static NSDictionary *storedProfile(void) {
    NSDictionary *value = [NSUserDefaults.standardUserDefaults dictionaryForKey:@"modelProfile"];
    NSString *provider = value[@"provider"], *base = value[@"base_url"], *model = value[@"model"];
    if ([provider isKindOfClass:NSString.class] && [base isKindOfClass:NSString.class] &&
        [model isKindOfClass:NSString.class]) {
        NSUInteger index = [protocols() indexOfObject:provider];
        if (index != NSNotFound) {
            NSDictionary *normalized = modelProfile((NSInteger)index, base, model);
            if (normalized) return normalized;
        }
    }
    return modelProfile(0, @"", @"");
}

static NSDictionary *keychainAccount(NSDictionary *profile) {
    NSString *account = [NSString stringWithFormat:@"%@\n%@", profile[@"provider"], profile[@"base_url"]];
    return @{(__bridge id)kSecClass: (__bridge id)kSecClassGenericPassword,
        (__bridge id)kSecAttrService: @"com.deskpulse.agent.model",
        (__bridge id)kSecAttrAccount: account};
}

static NSString *readKey(NSDictionary *profile) {
    NSMutableDictionary *query = [keychainAccount(profile) mutableCopy];
    query[(__bridge id)kSecReturnData] = @YES;
    query[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
    CFTypeRef value = NULL;
    if (SecItemCopyMatching((__bridge CFDictionaryRef)query, &value) != errSecSuccess) return nil;
    NSData *data = CFBridgingRelease(value);
    if (![data isKindOfClass:NSData.class] || data.length > 512) return nil;
    return [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
}

static BOOL storeKey(NSDictionary *profile, NSString *key) {
    NSData *data = [key dataUsingEncoding:NSUTF8StringEncoding];
    if (!data.length || data.length > 512 ||
        [key rangeOfCharacterFromSet:NSCharacterSet.controlCharacterSet].location != NSNotFound) return NO;
    NSDictionary *query = keychainAccount(profile);
    NSDictionary *change = @{(__bridge id)kSecValueData: data};
    OSStatus status = SecItemUpdate((__bridge CFDictionaryRef)query, (__bridge CFDictionaryRef)change);
    if (status == errSecItemNotFound) {
        NSMutableDictionary *added = [query mutableCopy];
        [added addEntriesFromDictionary:change];
        status = SecItemAdd((__bridge CFDictionaryRef)added, NULL);
    }
    return status == errSecSuccess;
}

@interface DPModelPanel ()
@property BOOL chinese;
@property BOOL running;
@property(strong) NSPopUpButton *protocol;
@property(strong) NSTextField *base;
@property(strong) NSTextField *model;
@property(strong) NSSecureTextField *key;
@property(strong) NSPanel *resultPanel;
@property(strong) NSTextView *result;
@end

@implementation DPModelPanel
- (instancetype)initWithChinese:(BOOL)chinese {
    self = [super init];
    if (self) _chinese = chinese;
    return self;
}

- (NSString *)zh:(NSString *)zh en:(NSString *)en { return self.chinese ? zh : en; }

- (void)notice:(NSString *)title detail:(NSString *)detail {
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = title;
    alert.informativeText = detail;
    [alert runModal];
}

- (void)loadProfile:(NSDictionary *)profile {
    NSUInteger index = [protocols() indexOfObject:profile[@"provider"]];
    [self.protocol selectItemAtIndex:index == NSNotFound ? 0 : (NSInteger)index];
    self.base.stringValue = profile[@"base_url"];
    self.model.stringValue = profile[@"model"];
    self.key.stringValue = @"";
}

- (void)changeProtocol:(id)sender {
    (void)sender;
    NSUInteger index = (NSUInteger)self.protocol.indexOfSelectedItem;
    if (index >= protocols().count) return;
    NSDictionary *current = storedProfile();
    if ([current[@"provider"] isEqualToString:protocols()[index]]) {
        [self loadProfile:current];
        return;
    }
    self.base.stringValue = endpoints()[index];
    self.model.stringValue = index == 0 ? @"deepseek-flash" : @"";
    self.key.stringValue = @"";
}

- (void)configure {
    NSAlert *dialog = [[NSAlert alloc] init];
    dialog.messageText = [self zh:@"模型设置" en:@"Model settings"];
    dialog.informativeText = [self zh:@"凭据保存在 macOS 钥匙串，按协议和接口地址隔离。" en:@"The Keychain stores a separate credential for each protocol and endpoint."];
    [dialog addButtonWithTitle:[self zh:@"保存" en:@"Save"]];
    [dialog addButtonWithTitle:[self zh:@"取消" en:@"Cancel"]];
    [dialog addButtonWithTitle:[self zh:@"清除此接口的 Key" en:@"Clear this endpoint’s key"]];
    self.protocol = [[NSPopUpButton alloc] initWithFrame:NSZeroRect pullsDown:NO];
    [self.protocol addItemsWithTitles:@[@"DeepSeek", @"Chat Completions", @"OpenAI Responses", @"Anthropic Messages"]];
    self.protocol.target = self;
    self.protocol.action = @selector(changeProtocol:);
    self.base = [NSTextField textFieldWithString:@""];
    self.model = [NSTextField textFieldWithString:@""];
    self.key = [[NSSecureTextField alloc] initWithFrame:NSZeroRect];
    self.key.placeholderString = [self zh:@"留空以保留此接口的 Key" en:@"Leave blank to keep the saved key"];
    [self loadProfile:storedProfile()];
    NSView *form = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, 350, 220)];
    NSStackView *stack = [[NSStackView alloc] initWithFrame:NSZeroRect];
    stack.translatesAutoresizingMaskIntoConstraints = NO;
    stack.orientation = NSUserInterfaceLayoutOrientationVertical;
    stack.alignment = NSLayoutAttributeLeading;
    stack.spacing = 4;
    [form addSubview:stack];
    [NSLayoutConstraint activateConstraints:@[
        [stack.topAnchor constraintEqualToAnchor:form.topAnchor],
        [stack.leadingAnchor constraintEqualToAnchor:form.leadingAnchor],
        [stack.trailingAnchor constraintEqualToAnchor:form.trailingAnchor],
    ]];
    NSArray *fields = @[self.protocol, self.base, self.model, self.key];
    NSArray *titles = @[[self zh:@"协议" en:@"Protocol"], [self zh:@"接口地址（HTTPS）" en:@"API base URL (HTTPS)"],
        [self zh:@"模型名称" en:@"Model ID"], @"API Key"];
    for (NSUInteger i = 0; i < fields.count; i++) {
        NSTextField *label = [NSTextField labelWithString:titles[i]];
        label.textColor = NSColor.secondaryLabelColor;
        [stack addArrangedSubview:label];
        NSView *field = fields[i];
        field.translatesAutoresizingMaskIntoConstraints = NO;
        [stack addArrangedSubview:field];
        [field.widthAnchor constraintEqualToAnchor:form.widthAnchor].active = YES;
        if (i+1 < fields.count) [stack setCustomSpacing:9 afterView:field];
    }
    dialog.accessoryView = form;
    NSInteger selected = [dialog runModal];
    if (selected == NSAlertFirstButtonReturn || selected == NSAlertThirdButtonReturn) {
        NSCharacterSet *space = NSCharacterSet.whitespaceAndNewlineCharacterSet;
        NSDictionary *profile = modelProfile(self.protocol.indexOfSelectedItem,
            [self.base.stringValue stringByTrimmingCharactersInSet:space],
            [self.model.stringValue stringByTrimmingCharactersInSet:space]);
        if (!profile) {
            [self notice:[self zh:@"模型配置有误" en:@"Invalid model settings"]
                detail:[self zh:@"请输入有效的 HTTPS 基础地址和模型名称。" en:@"Enter a valid HTTPS base URL and model ID."]];
        } else if (selected == NSAlertThirdButtonReturn) {
            OSStatus result = SecItemDelete((__bridge CFDictionaryRef)keychainAccount(profile));
            if (result != errSecSuccess && result != errSecItemNotFound) {
                [self notice:[self zh:@"无法清除凭据" en:@"Could not clear the credential"]
                    detail:[self zh:@"请在钥匙串访问中检查系统权限。" en:@"Check access in Keychain Access."]];
            }
        } else {
            NSString *key = [self.key.stringValue stringByTrimmingCharactersInSet:space];
            if (key.length && !storeKey(profile, key)) {
                [self notice:[self zh:@"无法保存凭据" en:@"Could not save the credential"]
                    detail:[self zh:@"请检查 Key 格式和钥匙串权限。" en:@"Check the key format and Keychain access."]];
            } else {
                [NSUserDefaults.standardUserDefaults setObject:profile forKey:@"modelProfile"];
            }
        }
    }
    self.key.stringValue = @"";
    self.key = nil;
    self.protocol = nil;
    self.base = nil;
    self.model = nil;
}

- (void)displayResult:(NSString *)text {
    if (!self.resultPanel) {
        NSRect frame = NSMakeRect(0, 0, 490, 360);
        self.resultPanel = [[NSPanel alloc] initWithContentRect:frame
            styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskResizable
            backing:NSBackingStoreBuffered defer:NO];
        self.resultPanel.title = [self zh:@"DeskPulse 诊断" en:@"DeskPulse diagnosis"];
        self.resultPanel.releasedWhenClosed = NO;
        NSScrollView *scroll = [[NSScrollView alloc] initWithFrame:frame];
        scroll.hasVerticalScroller = YES;
        self.result = [[NSTextView alloc] initWithFrame:frame];
        self.result.editable = NO;
        self.result.verticallyResizable = YES;
        self.result.autoresizingMask = NSViewWidthSizable;
        self.result.textContainerInset = NSMakeSize(18, 14);
        scroll.documentView = self.result;
        self.resultPanel.contentView = scroll;
        [self.resultPanel center];
    }
    NSDictionary *style = @{NSFontAttributeName: [NSFont systemFontOfSize:14],
        NSForegroundColorAttributeName: NSColor.labelColor};
    [self.result.textStorage setAttributedString:[[NSAttributedString alloc] initWithString:text attributes:style]];
    [self.result scrollRangeToVisible:NSMakeRange(0,0)];
    [self.resultPanel makeKeyAndOrderFront:nil];
}

- (NSString *)reportText:(NSDictionary *)report {
    NSMutableArray *lines = [NSMutableArray arrayWithObject:report[@"summary"] ?: @""];
    NSArray *findings = report[@"findings"];
    if (findings.count) {
        [lines addObject:[self zh:@"\n可能原因" en:@"\nPossible causes"]];
        for (NSDictionary *finding in findings) {
            NSArray *ids = finding[@"evidence_ids"];
            NSString *evidence = ids.count ? [ids componentsJoinedByString:@", "] : [self zh:@"待验证" en:@"Unverified"];
            [lines addObject:[NSString stringWithFormat:@"• %@  [%@]", finding[@"cause"], evidence]];
        }
    }
    for (NSArray *section in @[@[@"next_steps", [self zh:@"下一步" en:@"Next steps"]],
                               @[@"data_gaps", [self zh:@"证据不足" en:@"Evidence gaps"]]]) {
        NSArray *items = report[section[0]];
        if (!items.count) continue;
        [lines addObject:[@"\n" stringByAppendingString:section[1]]];
        for (NSString *item in items) [lines addObject:[@"• " stringByAppendingString:item]];
    }
    return [lines componentsJoinedByString:@"\n"];
}

- (void)diagnose:(NSArray<NSDictionary *> *)samples {
    if (self.running) {
        [self.resultPanel makeKeyAndOrderFront:nil];
        return;
    }
    if (samples.count < 2) {
        [self notice:[self zh:@"正在建立样本" en:@"Waiting for samples"]
            detail:[self zh:@"请过几秒后再试。" en:@"Try again in a few seconds."]];
        return;
    }
    NSDictionary *profile = storedProfile();
    NSAlert *confirm = [[NSAlert alloc] init];
    confirm.messageText = [self zh:@"发送本次诊断？" en:@"Send this diagnosis?"];
    confirm.informativeText = [NSString stringWithFormat:[self zh:@"接收方 %@\n模型 %@\n最近 30 秒内的 %lu 个 CPU 和估算内存样本。"
        en:@"Recipient %@\nModel %@\n%lu CPU and estimated memory samples from the last 30 seconds."],
        profile[@"base_url"], profile[@"model"], (unsigned long)samples.count];
    [confirm addButtonWithTitle:[self zh:@"发送" en:@"Send"]];
    [confirm addButtonWithTitle:[self zh:@"取消" en:@"Cancel"]];
    if ([confirm runModal] != NSAlertFirstButtonReturn) return;
    NSString *key = readKey(profile);
    if (!key.length) {
        [self notice:[self zh:@"模型凭据不可用" en:@"Model credential unavailable"]
            detail:[self zh:@"请在模型设置中添加此接口的 Key 或检查钥匙串权限。" en:@"Add a key for this endpoint in Model settings or check Keychain access."]];
        return;
    }
    NSData *request = [NSJSONSerialization dataWithJSONObject:@{@"profile": profile, @"key": key,
        @"language": self.chinese ? @"zh-CN" : @"en", @"window_seconds": @30, @"samples": samples} options:0 error:NULL];
    if (!request) return;
    self.running = YES;
    [self displayResult:[self zh:@"诊断中…" en:@"Diagnosing…"]];
    dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
        NSTask *task = [[NSTask alloc] init];
        task.executableURL = [NSBundle.mainBundle.bundleURL URLByAppendingPathComponent:@"Contents/Helpers/deskpulse-model"];
        task.standardInput = [NSPipe pipe];
        task.standardOutput = [NSPipe pipe];
        task.standardError = NSFileHandle.fileHandleWithNullDevice;
        NSString *failure = nil;
        NSDictionary *report = nil;
        if ([task launchAndReturnError:NULL]) {
            NSFileHandle *writer = [[task standardInput] fileHandleForWriting];
            BOOL written = [writer writeData:request error:NULL];
            [writer closeFile];
            NSData *output = [[[task standardOutput] fileHandleForReading] readDataToEndOfFile];
            [task waitUntilExit];
            if (written && task.terminationStatus == 0 && output.length <= 256 * 1024) {
                id value = [NSJSONSerialization JSONObjectWithData:output options:0 error:NULL];
                if ([value isKindOfClass:NSDictionary.class]) {
                    report = value[@"report"];
                    failure = value[@"error"];
                }
            }
        }
        dispatch_async(dispatch_get_main_queue(), ^{
            self.running = NO;
            if (report) {
                [self displayResult:[self reportText:report]];
            } else {
                NSString *message = [self zh:@"诊断失败，请检查模型设置和网络。" en:@"Diagnosis failed. Check the model settings and connection."];
                if (failure.length) message = [NSString stringWithFormat:@"%@\n%@", message, failure];
                [self displayResult:message];
            }
        });
    });
}
@end

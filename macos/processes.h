#import <AppKit/AppKit.h>

@interface DPProcessWindow : NSObject
- (instancetype)initWithChinese:(BOOL)chinese;
- (void)show;
@end

// Headless self-check reports capability only. Process names never leave the UI.
BOOL DPCheckProcessSampler(void);

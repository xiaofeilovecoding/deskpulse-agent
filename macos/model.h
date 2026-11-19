#import <AppKit/AppKit.h>

@interface DPModelPanel : NSObject
- (instancetype)initWithChinese:(BOOL)chinese;
- (void)configure;
- (void)diagnose:(NSArray<NSDictionary *> *)samples;
@end

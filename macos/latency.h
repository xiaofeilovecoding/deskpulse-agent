#import <Foundation/Foundation.h>

NSArray<NSString *> *DPNormalizeTargets(NSString *lines, NSString **error);
void DPProbeLatency(NSString *target, dispatch_queue_t queue, void (^complete)(NSNumber *milliseconds));
BOOL DPCheckLatency(void);

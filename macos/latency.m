#import "latency.h"
#import <Network/Network.h>
#include <arpa/inet.h>
#include <netinet/in.h>
#include <sys/socket.h>
#include <unistd.h>

static NSURLComponents *parseTarget(NSString *target) {
    if (target.length > 255 || [target rangeOfCharacterFromSet:NSCharacterSet.whitespaceAndNewlineCharacterSet].location != NSNotFound ||
        [target rangeOfCharacterFromSet:[NSCharacterSet characterSetWithCharactersInString:@"/\\?#@%"]].location != NSNotFound) {
        return nil;
    }
    NSURLComponents *parts = [NSURLComponents componentsWithString:[@"tcp://" stringByAppendingString:target]];
    NSString *host = parts.URL.host;
    if (!host.length || parts.user || parts.password || parts.path.length || parts.query || parts.fragment) return nil;
    NSInteger port = parts.port ? parts.port.integerValue : 443;
    if (port <= 0 || port > 65535 || [target hasSuffix:@":"]) return nil;
    parts.port = @(port);
    return parts;
}

NSArray<NSString *> *DPNormalizeTargets(NSString *lines, NSString **error) {
    if (error) *error = nil;
    if (lines.length > 4096) {
        if (error) *error = @"too_long";
        return nil;
    }
    NSMutableOrderedSet<NSString *> *targets = [NSMutableOrderedSet orderedSet];
    for (NSString *line in [lines componentsSeparatedByCharactersInSet:NSCharacterSet.newlineCharacterSet]) {
        NSString *target = [line stringByTrimmingCharactersInSet:NSCharacterSet.whitespaceCharacterSet];
        if (!target.length) continue;
        NSURLComponents *parts = parseTarget(target);
        if (!parts) {
            if (error) *error = @"invalid_target";
            return nil;
        }
        NSString *host = parts.URL.host.lowercaseString;
        if ([host containsString:@":"] && ![host hasPrefix:@"["]) host = [NSString stringWithFormat:@"[%@]", host];
        [targets addObject:[NSString stringWithFormat:@"%@:%@", host, parts.port]];
        if (targets.count > 8) {
            if (error) *error = @"too_many";
            return nil;
        }
    }
    return targets.array;
}

void DPProbeLatency(NSString *target, dispatch_queue_t queue, void (^complete)(NSNumber *milliseconds)) {
    NSURLComponents *parts = parseTarget(target);
    if (!parts) { dispatch_async(queue, ^{ complete(nil); }); return; }
    NSString *host = parts.URL.host;
    if ([host hasPrefix:@"["] && [host hasSuffix:@"]"]) host = [host substringWithRange:NSMakeRange(1, host.length-2)];
    nw_endpoint_t endpoint = nw_endpoint_create_host(host.UTF8String, parts.port.stringValue.UTF8String);
    nw_parameters_t params = nw_parameters_create_secure_tcp(NW_PARAMETERS_DISABLE_PROTOCOL, NW_PARAMETERS_DEFAULT_CONFIGURATION);
    nw_connection_t connection = nw_connection_create(endpoint, params);
    if (!connection) { dispatch_async(queue, ^{ complete(nil); }); return; }
    nw_connection_set_queue(connection, queue);
    __block BOOL finished = NO;
    double started = NSProcessInfo.processInfo.systemUptime;
    void (^finish)(NSNumber *) = ^(NSNumber *elapsed) {
        if (finished) return;
        finished = YES;
        nw_connection_set_state_changed_handler(connection, NULL);
        nw_connection_cancel(connection);
        complete(elapsed);
    };
    nw_connection_set_state_changed_handler(connection, ^(nw_connection_state_t state, nw_error_t error) {
        (void)error;
        if (state == nw_connection_state_ready) {
            finish(@(1000 * (NSProcessInfo.processInfo.systemUptime - started)));
        } else if (state == nw_connection_state_failed || state == nw_connection_state_cancelled) {
            finish(nil);
        }
    });
    // State updates and timeout always run on the same serial queue.
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC), queue, ^{ finish(nil); });
    nw_connection_start(connection);
}

BOOL DPCheckLatency(void) {
    NSString *error = nil;
    NSArray<NSString *> *targets = DPNormalizeTargets(@"LOCALHOST\nlocalhost:443\n[::1]:80", &error);
    if (error || targets.count != 2 || ![targets.firstObject isEqualToString:@"localhost:443"] ||
        DPNormalizeTargets(@"https://example.com", &error) || !error) {
        return NO;
    }
    int listener = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (listener < 0) return NO;
    struct sockaddr_in address = {0};
    address.sin_len = sizeof(address);
    address.sin_family = AF_INET;
    address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    socklen_t length = sizeof(address);
    if (bind(listener, (const struct sockaddr *)&address, length) < 0 ||
        listen(listener, 1) < 0 || getsockname(listener, (struct sockaddr *)&address, &length) < 0) {
        close(listener);
        return NO;
    }
    NSString *localTarget = [NSString stringWithFormat:@"127.0.0.1:%u", ntohs(address.sin_port)];
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    dispatch_queue_t serial = dispatch_queue_create("dev.deskpulse.latency-check", DISPATCH_QUEUE_SERIAL);
    __block BOOL passed = NO;
    DPProbeLatency(localTarget, serial, ^(NSNumber *ms) {
        passed = ms != nil && ms.doubleValue >= 0 && ms.doubleValue < 3000;
        dispatch_semaphore_signal(done);
    });
    long wait = dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 4 * NSEC_PER_SEC));
    close(listener);
    return wait == 0 && passed;
}

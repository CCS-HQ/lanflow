//go:build ios

package iosvpn

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#import <Foundation/Foundation.h>

// lanflowGroupContainer returns the path of the App Group container named
// by the app's Info.plist (LanflowAppGroup), or NULL; free it.
static char *lanflowGroupContainer(void) {
	@autoreleasepool {
		NSString *group = [NSBundle.mainBundle objectForInfoDictionaryKey:@"LanflowAppGroup"];
		if (![group isKindOfClass:NSString.class] || group.length == 0) {
			return NULL;
		}
		NSURL *url = [NSFileManager.defaultManager containerURLForSecurityApplicationGroupIdentifier:group];
		if (url == nil) {
			return NULL;
		}
		return strdup(url.path.fileSystemRepresentation);
	}
}
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"

	"lanflow/internal/i18n"
)

var (
	groupOnce sync.Once
	groupPath string
)

// groupContainer returns the App Group container's path. It is read with
// Foundation directly, not through the plugin: the service needs the
// controller's path while it is made, before the app runs.
func groupContainer() (string, error) {
	groupOnce.Do(func() {
		p := C.lanflowGroupContainer()
		if p == nil {
			return
		}
		groupPath = C.GoString(p)
		C.free(unsafe.Pointer(p))
	})
	if groupPath == "" {
		// Translated when asked: the language may change after the first call.
		return "", errors.New(i18n.Tf("App Group 容器不可用：App 须带有 Info.plist 中 %s 所指 App Group 的签名", AppGroupKey))
	}
	return groupPath, nil
}

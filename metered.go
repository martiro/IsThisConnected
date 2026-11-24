package main

// Metered-connection detection through INetworkCostManager (raw COM, no extra dependency).

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidNetworkListManager = windows.GUID{Data1: 0xDCB00C01, Data2: 0x570F, Data3: 0x4A9B, Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
	iidINetworkCostManager  = windows.GUID{Data1: 0xDCB00008, Data2: 0x570F, Data3: 0x4A9B, Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
)

const nlmCostUnrestricted = 0x1

// isMetered reports whether Windows considers the current connection metered.
// ok=false means the information is not available (caller ignores it).
func isMetered() (metered, ok bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil && err != syscall.Errno(1) { // S_FALSE = already initialized
		return false, false
	}
	defer windows.CoUninitialize()
	var obj unsafe.Pointer // COM interface pointer filled by CoCreateInstance
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidNetworkListManager)), 0,
		0x1|0x4, // CLSCTX_INPROC_SERVER|CLSCTX_LOCAL_SERVER
		uintptr(unsafe.Pointer(&iidINetworkCostManager)), uintptr(unsafe.Pointer(&obj)))
	if r != 0 || obj == nil {
		return false, false
	}
	vtbl := *(*[6]uintptr)(*(*unsafe.Pointer)(obj))
	defer syscall.SyscallN(vtbl[2], uintptr(obj)) // Release
	var cost uint32
	r, _, _ = syscall.SyscallN(vtbl[3], uintptr(obj), uintptr(unsafe.Pointer(&cost)), 0) // GetCost(&cost, NULL)
	if r != 0 {
		return false, false
	}
	return cost&nlmCostUnrestricted == 0, true
}

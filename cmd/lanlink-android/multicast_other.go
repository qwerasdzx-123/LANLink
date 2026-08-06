//go:build !android

package main

// AcquireMulticastLock 非 Android 平台为空实现，仅 Android 上有 JNI 桥接。
func AcquireMulticastLock() {}

//go:build android

package main

/*
#cgo LDFLAGS: -llog

#include <jni.h>
#include <android/log.h>

// 在 JVM 线程中申请 WifiManager.MulticastLock。
// Context.WIFI_SERVICE 的常量值就是字符串 "wifi"，
// 拿到 WifiManager 后 createMulticastLock("lanlink") 并 acquire()，
// 即可让 Android 不再过滤 239.x 组播包。
static void lanlink_acquire_multicast_lock(JNIEnv* env, jobject context) {
    jclass contextClass = (*env)->GetObjectClass(env, context);
    jmethodID getSystemService = (*env)->GetMethodID(env, contextClass,
        "getSystemService", "(Ljava/lang/String;)Ljava/lang/Object;");
    if (getSystemService == 0) {
        __android_log_print(ANDROID_LOG_WARN, "LANLink", "getSystemService not found");
        (*env)->ExceptionClear(env);
        return;
    }

    jstring svcName = (*env)->NewStringUTF(env, "wifi");
    jobject wifiManager = (*env)->CallObjectMethod(env, context, getSystemService, svcName);
    (*env)->DeleteLocalRef(env, svcName);
    if (wifiManager == 0) {
        __android_log_print(ANDROID_LOG_WARN, "LANLink", "WifiManager is null");
        return;
    }

    jclass wmClass = (*env)->GetObjectClass(env, wifiManager);
    // 注意：MulticastLock 是 WifiManager 的内部类，
    // JNI 描述符里内部类用 '$' 连接，必须写成 WifiManager$MulticastLock，
    // 否则 GetMethodID 失败并留下一个未捕获的 JNI 异常，直接拖崩 JVM。
    jmethodID createLock = (*env)->GetMethodID(env, wmClass,
        "createMulticastLock", "(Ljava/lang/String;)Landroid/net/wifi/WifiManager$MulticastLock;");
    if (createLock == 0) {
        __android_log_print(ANDROID_LOG_WARN, "LANLink", "createMulticastLock not found");
        (*env)->ExceptionClear(env);
        (*env)->DeleteLocalRef(env, wifiManager);
        return;
    }

    jstring tag = (*env)->NewStringUTF(env, "lanlink");
    jobject lock = (*env)->CallObjectMethod(env, wifiManager, createLock, tag);
    (*env)->DeleteLocalRef(env, tag);
    (*env)->DeleteLocalRef(env, wifiManager);
    if (lock == 0) {
        __android_log_print(ANDROID_LOG_WARN, "LANLink", "MulticastLock is null");
        return;
    }

    jclass lockClass = (*env)->GetObjectClass(env, lock);
    jmethodID acquire = (*env)->GetMethodID(env, lockClass, "acquire", "()V");
    if (acquire != 0) {
        (*env)->CallVoidMethod(env, lock, acquire);
        (*env)->ExceptionClear(env);
        __android_log_print(ANDROID_LOG_INFO, "LANLink", "MulticastLock acquired");
    } else {
        (*env)->ExceptionClear(env);
    }
    (*env)->DeleteLocalRef(env, lock);
}
*/
import "C"

import (
	"fyne.io/fyne/v2/driver"
	"unsafe"
)

// AcquireMulticastLock 申请 WifiManager.MulticastLock。
// 这样 Android 不会再丢弃局域网内的 239.x 组播包，
// 手机端能稳定收到电脑端通过组播发送的发现报文，从而提升双向发现成功率。
func AcquireMulticastLock() {
	_ = driver.RunNative(func(data any) error {
		ac := data.(*driver.AndroidContext)
		C.lanlink_acquire_multicast_lock((*C.JNIEnv)(unsafe.Pointer(ac.Env)), C.jobject(unsafe.Pointer(ac.Ctx)))
		return nil
	})
}

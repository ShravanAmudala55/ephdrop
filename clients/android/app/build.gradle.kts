plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "dev.ephdrop"
    compileSdk = 34

    defaultConfig {
        applicationId = "dev.ephdrop.app"
        minSdk = 29
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
}

dependencies {
    // The Go program, built from core/mobile by build-lib.sh
    implementation(files("libs/ephdrop.aar"))
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.activity:activity-ktx:1.9.2")
    // QR scanner from Google Play services. Needs no camera permission.
    implementation("com.google.android.gms:play-services-code-scanner:16.1.0")
}

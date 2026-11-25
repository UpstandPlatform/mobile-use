plugins { id("com.android.application") }

android {
    namespace = "org.mobileuse.control"
    compileSdk = 36
    defaultConfig {
        applicationId = "org.mobileuse.control"
        minSdk = 34
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0"
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

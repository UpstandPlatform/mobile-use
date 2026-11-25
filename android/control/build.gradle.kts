plugins { id("com.android.application") }

android {
    namespace = "org.mobileuse.control"
    compileSdk = 36
    defaultConfig {
        applicationId = "org.mobileuse.control"
        minSdk = 34
        targetSdk = 36
        versionCode = providers.gradleProperty("mobileUseVersionCode").map(String::toInt).getOrElse(1)
        versionName = providers.gradleProperty("mobileUseVersion").getOrElse("0.1.0-dev")
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

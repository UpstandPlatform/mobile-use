plugins { id("com.android.application") }

android {
    namespace = "org.mobileuse.fixture"
    compileSdk = 36
    defaultConfig {
        applicationId = "org.mobileuse.fixture"
        minSdk = 34
        targetSdk = 36
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

androidComponents {
    beforeVariants(selector().withBuildType("release")) { it.enable = false }
}

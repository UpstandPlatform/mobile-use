package org.mobileuse.companion

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class BrowserAddressTest {
    @Test
    fun recoversChromeAddressWithoutVisibleScheme() {
        assertEquals("https://example.com", browserAddressFromLabel("example.com"))
    }

    @Test
    fun keepsExplicitHttpAddress() {
        assertEquals("http://localhost:8080/path", browserAddressFromLabel("http://localhost:8080/path"))
    }

    @Test
    fun ignoresSearchTermsAndCredentials() {
        assertNull(browserAddressFromLabel("open instagram"))
        assertNull(browserAddressFromLabel("https://name:secret@example.com"))
    }
}

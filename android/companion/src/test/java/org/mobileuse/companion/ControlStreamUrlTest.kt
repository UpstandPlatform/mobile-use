package org.mobileuse.companion

import org.junit.Assert.assertEquals
import org.junit.Test

class ControlStreamUrlTest {
    @Test
    fun httpsOriginProducesWebSocketRequestUrl() {
        assertEquals(
            "https://phone.example:8444/companion/sessions/session-1/control",
            controlStreamUrl("https://phone.example:8444/", "session-1"),
        )
    }

    @Test
    fun httpOriginRemainsHttpForLocalDevelopment() {
        assertEquals(
            "http://127.0.0.1:8444/companion/sessions/session-1/control",
            controlStreamUrl("http://127.0.0.1:8444", "session-1"),
        )
    }
}

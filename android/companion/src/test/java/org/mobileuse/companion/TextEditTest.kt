package org.mobileuse.companion

import org.junit.Assert.assertEquals
import org.junit.Test

class TextEditTest {
    @Test
    fun insertsAtTheCurrentCursor() {
        assertEquals(TextEdit("he!llo", 3), insertAtSelection("hello", 2, 2, "!"))
    }

    @Test
    fun replacesTheCurrentSelection() {
        assertEquals(TextEdit("heyo", 3), insertAtSelection("hello", 2, 4, "y"))
    }

    @Test
    fun backspaceDeletesTheSelection() {
        assertEquals(TextEdit("ho", 1), backspaceAtSelection("hello", 1, 4))
    }

    @Test
    fun backspaceDeletesOneUnicodeCodePoint() {
        assertEquals(TextEdit("ab", 1), backspaceAtSelection("a😀b", 3, 3))
    }
}

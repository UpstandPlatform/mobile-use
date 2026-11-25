package org.mobileuse.companion

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.net.URI
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

data class Pairing(val origin: String, val token: String)

class PairingStore(context: Context) {
    private val preferences = context.getSharedPreferences("pairing", Context.MODE_PRIVATE)

    fun read(): Pairing? = runCatching {
        val origin = preferences.getString("origin", null) ?: return null
        val sealed = Base64.decode(preferences.getString("token", null) ?: return null, Base64.NO_WRAP)
        val iv = Base64.decode(preferences.getString("iv", null) ?: return null, Base64.NO_WRAP)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, iv))
        cipher.updateAAD(origin.toByteArray())
        Pairing(origin, cipher.doFinal(sealed).toString(Charsets.UTF_8))
    }.getOrNull()

    fun save(pairing: Pairing) {
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key())
        cipher.updateAAD(pairing.origin.toByteArray())
        val sealed = cipher.doFinal(pairing.token.toByteArray())
        check(preferences.edit()
            .putString("origin", pairing.origin)
            .putString("token", Base64.encodeToString(sealed, Base64.NO_WRAP))
            .putString("iv", Base64.encodeToString(cipher.iv, Base64.NO_WRAP))
            .commit()) { "Pairing could not be saved." }
    }

    fun clear() { preferences.edit().clear().apply() }

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        val existing = store.getKey(KEY_ALIAS, null) as? SecretKey
        if (existing != null) return existing
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
            .build())
        return generator.generateKey()
    }

    companion object { private const val KEY_ALIAS = "mobile-use-pairing" }
}

fun normalizedOrigin(value: String): String {
    val origin = URI(value.trim().trimEnd('/'))
    require(origin.scheme == "https" && origin.host != null && origin.userInfo == null &&
        origin.rawPath.isNullOrEmpty() && origin.rawQuery == null && origin.rawFragment == null) {
        "Enter the HTTPS server address shown in the console."
    }
    return origin.toASCIIString()
}

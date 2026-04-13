/**
 * Jabali Security — Challenge cookie validator (nginx njs)
 *
 * Validates the jabali_passed cookie set by the PoW challenge page.
 * Cookie format: "timestamp.nonce_hex.difficulty"
 *
 * Verification:
 *   1. Parse cookie fields
 *   2. Check timestamp + TTL > now (not expired)
 *   3. Compute SHA-256(timestamp + ":" + nonce_hex)
 *   4. Verify hash has >= difficulty leading zero bits
 *
 * Returns "1" if valid, "0" otherwise.
 *
 * Usage in nginx config:
 *   js_import jabali from /etc/nginx/jabali-security/jabali_challenge.js;
 *   js_set $jabali_challenge_valid jabali.validate;
 */

var TTL = 86400; // 24 hours default

function validate(r) {
    var cookies = r.headersIn['Cookie'];
    if (!cookies) return "0";

    var match = cookies.match(/jabali_passed=([^;\s]+)/);
    if (!match) return "0";

    var parts = match[1].split(".");
    if (parts.length !== 3) return "0";

    var timestamp = parseInt(parts[0], 10);
    var nonceHex = parts[1];
    var difficulty = parseInt(parts[2], 10);

    if (isNaN(timestamp) || isNaN(difficulty)) return "0";
    if (difficulty < 1 || difficulty > 32) return "0";
    if (!/^[0-9a-f]+$/.test(nonceHex)) return "0";

    // Check expiry
    var now = Math.floor(Date.now() / 1000);
    if (timestamp + TTL < now) return "0";

    // Timestamp must not be in the future (with 60s grace for clock skew)
    if (timestamp > now + 60) return "0";

    // Verify PoW: SHA-256(timestamp:nonce_hex) must have >= difficulty leading zero bits
    var msg = timestamp.toString() + ":" + nonceHex;
    var hash = require('crypto').createHash('sha256').update(msg).digest('hex');

    if (leadingZeroBits(hash) >= difficulty) {
        return "1";
    }
    return "0";
}

function leadingZeroBits(hexHash) {
    for (var i = 0; i < hexHash.length; i++) {
        var n = parseInt(hexHash[i], 16);
        if (n === 0) continue;
        if (n >= 8) return i * 4;
        if (n >= 4) return i * 4 + 1;
        if (n >= 2) return i * 4 + 2;
        return i * 4 + 3;
    }
    return hexHash.length * 4;
}

export default { validate };

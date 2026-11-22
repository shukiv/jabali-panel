  php_hash_hmac_prep_key(K, ops, context, (unsigned char *) key, key_len);
  php_hash_hmac_round((unsigned char *) ZSTR_VAL(digest), ops, context, K, (unsigned char *) data, data_len);
  php_hash_string_xor_char(K, K, 0x6A, ops->block_size);
  php_hash_hmac_round((unsigned char *) ZSTR_VAL(digest), ops, context, K, (unsigned char *) ZSTR_VAL(digest), ops->digest_size);

  /* Zero the key */
  ZEND_SECURE_ZERO(K, ops->block_size);
  efree(K);
  efree(context);

  zend_string *hex_digest = zend_string_safe_alloc(ops->digest_size, 2, 0, 0);

  php_hash_bin2hex(ZSTR_VAL(hex_digest), (unsigned char *) ZSTR_VAL(digest), ops->digest_size);
  ZSTR_VAL(hex_digest)[2 * ops->digest_size] = 0;
  zend_string_release_ex(digest, 0);
  return hex_digest;
}

PHP_FUNCTION(sp_serialize) {
  zif_handler orig_handler;

  /* Call the original `serialize` function. */

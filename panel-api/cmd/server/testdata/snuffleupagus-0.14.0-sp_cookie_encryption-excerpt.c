    ) {
  int found = 0;
  zend_string *key;
  zval *value;

  ZEND_HASH_FOREACH_STR_KEY_VAL(Z_ARRVAL_P(options), key, value) {
    if (key) {
      if (zend_string_equals_literal_ci(key, "expires")) {
        *expires = zval_get_long(value);
        found++;
      } else if (zend_string_equals_literal_ci(key, "path")) {
        *path = zval_get_string(value);
        found++;
      } else if (zend_string_equals_literal_ci(key, "domain")) {
        *domain = zval_get_string(value);
        found++;
      } else if (zend_string_equals_literal_ci(key, "secure")) {
        *secure = zval_is_true(value);
        found++;
      } else if (zend_string_equals_literal_ci(key, "httponly")) {
        *httponly = zval_is_true(value);
        found++;
      } else if (zend_string_equals_literal_ci(key, "samesite")) {
        *samesite = zval_get_string(value);
        found++;
#if PHP_VERSION_ID >= 80500
      } else if (zend_string_equals_literal_ci(key, "partitioned")) {
        *partitioned = zval_is_true(value);
        found++;
#endif
      } else {
        php_error_docref(NULL, E_WARNING,
                         "Unrecognized key '%s' found in the options array",
                         ZSTR_VAL(key));
      }
    } else {

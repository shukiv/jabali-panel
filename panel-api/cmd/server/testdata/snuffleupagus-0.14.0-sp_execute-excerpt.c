              disabled_functions_hooked);
          break;
        case ZEND_REQUIRE:
          is_builtin_matching(
              zend_filename, "require", "inclusion path",
              zend_hash_str_find_ptr(disabled_functions_hooked, ZEND_STRL("require")),
              disabled_functions_hooked);
          break;
        case ZEND_REQUIRE_ONCE:
          is_builtin_matching(
              zend_filename, "require_once", "inclusion path",
              zend_hash_str_find_ptr(disabled_functions_hooked, ZEND_STRL("require_once")),
              disabled_functions_hooked);
          break;
        case ZEND_INCLUDE_ONCE:
          is_builtin_matching(
              zend_filename, "include_once", "inclusion path",
              zend_hash_str_find_ptr(disabled_functions_hooked, ZEND_STRL("include_once")),
              disabled_functions_hooked);
          break;
          EMPTY_SWITCH_DEFAULT_CASE();  // LCOV_EXCL_LINE
      }
  }
}

#if PHP_VERSION_ID < 80100

static int sp_stream_open(const char *filename, zend_file_handle *handle) {
  zend_string *zend_filename = zend_string_init(filename, strlen(filename), 0);

  sp_stream_open_checks(zend_filename, handle);

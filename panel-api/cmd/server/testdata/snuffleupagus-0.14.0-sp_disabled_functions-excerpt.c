      }
    }
  }
  return false;
}

static inline const char* get_fn_arg_name(zend_function *fn, uint32_t i) {
  if (fn->type == ZEND_USER_FUNCTION || (fn->common.fn_flags & ZEND_ACC_USER_ARG_INFO)) {
    return ZSTR_VAL(fn->op_array.arg_info[i].name);
  } else {
    return fn->internal_function.arg_info[i].name;
  }
}

static bool is_param_matching(zend_execute_data* execute_data,
                              sp_disabled_function const* const config_node,
                              zend_string* builtin_param,
                              const char* builtin_param_name,
                              const char** arg_name,
                              zend_string** arg_value_str) {
  // builtin functions

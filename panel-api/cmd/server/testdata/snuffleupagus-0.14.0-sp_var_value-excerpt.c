#include "php_snuffleupagus.h"

static zval *get_param_var(const zend_execute_data *const ed,
                           const char *const var_name, bool print) {
  unsigned int nb_param = ed->func->common.num_args;

  for (unsigned int i = 0; i < nb_param; i++) {
    const char *arg_name;
    if (ZEND_USER_CODE(ed->func->type)) {
      arg_name = ZSTR_VAL(ed->func->common.arg_info[i].name);
    } else {
      arg_name = ed->func->internal_function.arg_info[i].name;
    }
    if (0 == strcmp(arg_name, var_name)) {
      return ZEND_CALL_VAR_NUM(ed, i);
    }
    if (print == true) {
      sp_log_warn("config", "  - %d parameter's name: '%s'", i, arg_name);
    }
  }
  return NULL;
}

static zval *get_local_var(zend_execute_data *ed, const char *const var_name) {
  zend_execute_data *orig_execute_data = ed;

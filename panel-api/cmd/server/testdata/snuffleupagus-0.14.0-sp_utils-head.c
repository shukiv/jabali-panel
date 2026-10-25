#include "php_snuffleupagus.h"

#ifndef MIN
#define MIN(a,b) (((a)<(b))?(a):(b))
#endif

#ifndef PATH_MAX
# define PATH_MAX 4096
#endif

static char const* const default_ipaddr = "0.0.0.0";
const char* get_ipaddr() {
  const char* client_ip = getenv("REMOTE_ADDR");
  if (client_ip) {
    return client_ip;
  }

  const char* fwd_ip = getenv("HTTP_X_FORWARDED_FOR");
  if (fwd_ip) {
    return fwd_ip;
  }

  return default_ipaddr;
}

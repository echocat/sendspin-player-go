#ifndef SENDSPIN_WASAPI_H
#define SENDSPIN_WASAPI_H
#include <stdint.h>
typedef struct sendspin_wa sendspin_wa;
sendspin_wa *sendspin_wa_open(uintptr_t token, const char *endpoint, int rate, int channels, int32_t *error);
int32_t sendspin_wa_start(sendspin_wa *p);
int32_t sendspin_wa_stop(sendspin_wa *p);
int32_t sendspin_wa_check(sendspin_wa *p);
void sendspin_wa_close(sendspin_wa *p);
#endif

#ifndef SENDSPIN_COREAUDIO_H
#define SENDSPIN_COREAUDIO_H
#include <stdint.h>
typedef struct sendspin_ca sendspin_ca;
sendspin_ca *sendspin_ca_open(uintptr_t token, const char *uid, int rate, int channels, int32_t *error);
int32_t sendspin_ca_start(sendspin_ca *p);
int32_t sendspin_ca_stop(sendspin_ca *p);
int32_t sendspin_ca_check(sendspin_ca *p);
int32_t sendspin_ca_close(sendspin_ca *p);
#endif

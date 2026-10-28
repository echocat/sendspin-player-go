#ifndef SENDSPIN_PIPEWIRE_H
#define SENDSPIN_PIPEWIRE_H
#include <stdint.h>
typedef struct sendspin_pw sendspin_pw;
sendspin_pw *sendspin_pw_open(uintptr_t token, const char *target, int rate, int channels, int *error);
int sendspin_pw_start(sendspin_pw *p);
int sendspin_pw_stop(sendspin_pw *p);
int sendspin_pw_check(sendspin_pw *p);
void sendspin_pw_close(sendspin_pw *p);
#endif

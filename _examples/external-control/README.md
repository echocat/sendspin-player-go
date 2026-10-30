# Host-managed volume and mute

A fake host adapter for the SDK's external player controls. It binds volume/mute
setters and reports independently observed state without connecting to a server
or opening audio hardware.

Run from the repository root:

```shell
mise example external-control
```

The output walks through writable controls, temporary unavailability, restored
readback, a read-only target, a replacement target with volume 80, and finally
fixed/unknown state. The example takes no command-line options.

The host keeps the binding returned by each report and captures a concrete
device in its setters. Selecting a target or reporting its state does not write
to that device. External mode disables software volume/mute; actual hardware
adapters belong to the application.

See [host-managed volume and mute](../../docs/usage.md#host-managed-volume-and-mute)
for the integration contract.

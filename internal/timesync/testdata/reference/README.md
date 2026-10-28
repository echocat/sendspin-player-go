# Pinned time-filter oracle

`sendspin_time_filter.cpp` and `.h` are **byte-for-byte upstream copies** from
Sendspin/time-filter commit `531e2707a6e82f30efe335b0f87eaa4b29390021`.
Their Apache-2.0 headers are preserved; see the root `LICENSES/Apache-2.0.txt`.
Tests verify the exact pinned Git blob IDs, as well as SHA-256 provenance in the
frozen fixture. `.gitattributes` fixes LF endings on Windows checkouts.

`driver.cpp` is our MIT-licensed, single-threaded test driver. A read-only subclass
observes the upstream protected counter/drift state. It does not change any filter
equation, state, or default parameter. The extra readiness column checks the SDK's
own policy against the reference counter and covariance, not an upstream API.

## Protocol and values

Driver input is deterministic ASCII, one operation per line:

- `r`: reset the reference; the Go comparison constructs `New()`.
- `u measurement max_error at`: call upstream `update`.
- `q client_timestamp server_timestamp`: query both time mappings and diagnostics.

Each query produces eight integer columns, stored as one JSON array:

1. computed server time, in µs;
2. computed client time, in µs;
3. initialized flag;
4. rounded offset standard deviation, in µs;
5. rounded covariance, in µs²;
6. accepted-sample counter (saturates at the reference's forgetting threshold);
7. drift-significance flag;
8. SDK readiness predicate (`count >= 3 && covariance <= 250000`).

Before initialization, columns 4/5 are **sentinels**, not calls to the reference
integer getters: converting its initial infinity to int64 is undefined in C++.
The Go API deliberately retains infinity in `Error()` before initialization.
Other trace values stay in well-defined C++ arithmetic ranges. Arbitrary int64
boundary arithmetic is tested separately for the Go NTP sample helper using
`math/big`, not by invoking undefined operations in the C++ oracle.

## Reproduction

Normal `go test ./...` replays all 22,789 recorded queries in 16 scenarios without
a C++ compiler. Every scenario's complete input stream has a SHA-256 digest.
Changes to scenarios, oracle sources, driver, or fixed arithmetic flags require
an intentional fixture update.

```text
mise run test:timefilter
mise run test:timefilter:reference
mise run test:timefilter:update
```

The latter two require a native GCC/Clang-compatible C++17 compiler. `CXX` can
select an executable path, including paths containing spaces; it is not a shell
command. Otherwise tests look for `c++`, `g++`, then `clang++`. Missing compilers
fail the live test instead of silently skipping it. No download occurs in tests.
On Windows, use native MinGW-w64 or Clang with its C++ headers/libraries/runtime;
MSVC `cl` flags are not supported by this driver.

Setting `SENDSPIN_REFERENCE_UBSAN=1` additionally enables undefined-behavior and
float-to-integer conversion sanitizers. The committed fixture records the GNU
16.2.0 compiler fingerprint, source hashes, and instrumentation used to create it.
Live results always come from the newly compiled C++ executable; update mode
never writes Go predictions as expected values.

The fixed flags disable fast-math and floating-point contraction. Both time
mappings allow **1 µs** cross-compiler/architecture rounding differences; the
explicit binary64-precision-boundary scenario allows **2 µs**. Rounded error and
covariance permit one integer-unit difference. Initialization, counter, drift
decision, and readiness must match exactly. These tolerances are test policy,
not a relaxation of the spec's physical ±1ms output requirement.

Scenarios cover initialization, rejected/duplicate/non-monotonic samples,
positive/negative offset and drift, jitter/uncertainty changes, early/late clock
steps, outliers, six-hour gaps, drift changes, minimum-uncertainty bursts, long
uptime, binary64 precision, readiness recovery, and reset/reconvergence. Matching
these finite traces establishes port parity for those cases, not proof of every
possible input or correctness of the reference algorithm itself.

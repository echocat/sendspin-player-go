// Copyright 2026 Gregor Noczinski. Licensed under the repository's MIT license.
// This driver observes the unchanged Apache-2.0 reference; it never changes its state.
#include "sendspin_time_filter.h"

#include <cinttypes>
#include <cstdio>

class ObservedFilter : public SendspinTimeFilter {
public:
    struct State { unsigned count; bool drift; bool ready; };
    State inspect() const {
        std::lock_guard<std::mutex> lock(state_mutex_);
        return {count_, use_drift_, count_ >= 3 && offset_covariance_ <= 250000.0};
    }
};

int main() {
    ObservedFilter filter;
    char operation;
    while (std::scanf(" %c", &operation) == 1) {
        int64_t a, b, c;
        if (operation == 'r') {
            filter.reset();
        } else if (operation == 'u') {
            if (std::scanf("%" SCNd64 " %" SCNd64 " %" SCNd64, &a, &b, &c) != 3) return 2;
            filter.update(a, b, c);
        } else if (operation == 'q') {
            if (std::scanf("%" SCNd64 " %" SCNd64, &a, &b) != 2) return 2;
            const auto state = filter.inspect();
            // The upstream integer getters convert infinity before initialization;
            // avoid that undefined float-to-integer cast. Zero is a sentinel only
            // when the explicit initialized flag is false.
            const int64_t error = state.count ? filter.get_error() : 0;
            const int64_t covariance = state.count ? filter.get_covariance() : 0;
            std::printf("%" PRId64 " %" PRId64 " %d %" PRId64 " %" PRId64 " %u %d %d\n",
                filter.compute_server_time(a), filter.compute_client_time(b),
                state.count != 0, error, covariance, state.count, state.drift, state.ready);
        } else {
            return 2;
        }
    }
    return std::ferror(stdin) ? 3 : 0;
}

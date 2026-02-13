<x-filament-panels::page>
    <div
        x-data="{
            interval: @js($refreshInterval),
            timer: null,
            init() {
                this.startPolling();
                Livewire.on('refresh-interval-changed', (data) => {
                    this.interval = data.interval;
                    this.startPolling();
                });
            },
            startPolling() {
                if (this.timer) {
                    clearInterval(this.timer);
                    this.timer = null;
                }
                if (this.interval === 'off') return;

                const ms = this.getMilliseconds(this.interval);
                if (ms > 0) {
                    this.timer = setInterval(() => {
                        $wire.loadMetrics();
                    }, ms);
                }
            },
            getMilliseconds(interval) {
                const map = { '5s': 5000, '10s': 10000, '30s': 30000, '60s': 60000 };
                return map[interval] || 0;
            }
        }"
        x-init="init()"
        @beforeunload.window="if (timer) clearInterval(timer)"
    >
        {{-- Processes Table --}}
        {{ $this->table }}
    </div>
</x-filament-panels::page>

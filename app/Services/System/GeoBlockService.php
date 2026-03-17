<?php

declare(strict_types=1);

namespace App\Services\System;

use App\Models\GeoBlockRule;
use App\Services\Agent\AgentClient;

class GeoBlockService
{
    public function applyCurrentRules(): void
    {
        $rules = GeoBlockRule::query()
            ->where('is_active', true)
            ->get(['country_code', 'action'])
            ->map(static function ($rule): array {
                return [
                    'country_code' => strtoupper((string) $rule->country_code),
                    'action' => $rule->action,
                    'is_active' => true,
                ];
            })
            ->values()
            ->toArray();

        $agent = app(AgentClient::class);
        $agent->geoApplyRules($rules);
    }
}

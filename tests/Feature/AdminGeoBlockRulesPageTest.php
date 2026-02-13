<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class AdminGeoBlockRulesPageTest extends TestCase
{
    use RefreshDatabase;

    public function test_geo_block_rules_page_is_accessible(): void
    {
        $admin = User::factory()->admin()->create();

        $response = $this->actingAs($admin, 'admin')->get('/jabali-admin/geo-block-rules');

        $response->assertStatus(200);
    }
}

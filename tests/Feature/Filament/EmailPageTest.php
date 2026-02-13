<?php

declare(strict_types=1);

namespace Tests\Feature\Filament;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class EmailPageTest extends TestCase
{
    use RefreshDatabase;

    public function test_email_page_is_accessible(): void
    {
        $user = User::factory()->create([
            'username' => 'emailuser',
        ]);

        $response = $this->actingAs($user)
            ->get('/jabali-panel/email');

        $response->assertStatus(200);
    }
}

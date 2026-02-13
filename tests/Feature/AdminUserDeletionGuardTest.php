<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class AdminUserDeletionGuardTest extends TestCase
{
    use RefreshDatabase;

    public function test_primary_admin_user_cannot_be_deleted(): void
    {
        $primaryAdmin = User::factory()->admin()->create();
        $otherAdmin = User::factory()->admin()->create();

        try {
            $primaryAdmin->delete();
            $this->fail('Primary admin deletion should be blocked.');
        } catch (\RuntimeException $exception) {
            $this->assertDatabaseHas('users', [
                'id' => $primaryAdmin->id,
            ]);
            $this->assertSame('Primary admin account cannot be deleted.', $exception->getMessage());
        }

        $otherAdmin->delete();

        $this->assertDatabaseMissing('users', [
            'id' => $otherAdmin->id,
        ]);
    }
}

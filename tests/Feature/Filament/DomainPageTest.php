<?php

namespace Tests\Feature\Filament;

use App\Models\Domain;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class DomainPageTest extends TestCase
{
    use RefreshDatabase;

    protected User $user;

    protected function setUp(): void
    {
        parent::setUp();

        $this->user = User::factory()->create([
            'username' => 'testuser',
        ]);

        // Disable observers to avoid agent calls in tests
        Domain::unsetEventDispatcher();
    }

    protected function tearDown(): void
    {
        // Re-enable observers after tests
        Domain::setEventDispatcher(app('events'));
        parent::tearDown();
    }

    public function test_domains_page_is_accessible(): void
    {
        $response = $this->actingAs($this->user)
            ->get('/jabali-panel/domains');

        $response->assertStatus(200);
    }

    public function test_domain_belongs_to_correct_user(): void
    {
        $domain = Domain::factory()->create([
            'user_id' => $this->user->id,
            'domain' => 'example.com',
        ]);

        $this->assertEquals($this->user->id, $domain->user_id);
        $this->assertDatabaseHas('domains', [
            'user_id' => $this->user->id,
            'domain' => 'example.com',
        ]);
    }

    public function test_domain_scoping_filters_by_user(): void
    {
        $otherUser = User::factory()->create([
            'username' => 'otheruser',
        ]);

        Domain::factory()->create([
            'user_id' => $this->user->id,
            'domain' => 'mysite.com',
        ]);

        Domain::factory()->create([
            'user_id' => $otherUser->id,
            'domain' => 'other-site.com',
        ]);

        $userDomains = Domain::where('user_id', $this->user->id)->pluck('domain');
        $this->assertContains('mysite.com', $userDomains);
        $this->assertNotContains('other-site.com', $userDomains);
    }

    public function test_user_has_domains_relationship(): void
    {
        Domain::factory()->count(3)->create([
            'user_id' => $this->user->id,
        ]);

        $this->user->refresh();
        $this->assertCount(3, $this->user->domains);
    }
}

<?php

declare(strict_types=1);

namespace Tests\Feature;

use App\Models\Domain;
use App\Models\EmailDomain;
use App\Models\Mailbox;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

class WebmailSsoViewTest extends TestCase
{
    use RefreshDatabase;

    public function test_webmail_sso_shows_login_required_for_migrated_mailbox(): void
    {
        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->createQuietly([
            'domain' => 'migrated-example.test',
        ]);
        $emailDomain = EmailDomain::create([
            'domain_id' => $domain->id,
            'is_active' => true,
        ]);
        $mailbox = Mailbox::create([
            'email_domain_id' => $emailDomain->id,
            'user_id' => $user->id,
            'local_part' => 'info',
            'password_hash' => '{CRYPT}$6$examplehash',
            'name' => 'Info',
        ]);

        $response = $this->actingAs($user)->get(route('webmail.sso', $mailbox));

        if (file_exists('/etc/jabali/roundcube-sso.conf')) {
            $response->assertStatus(302);
            $location = $response->headers->get('Location');
            $this->assertNotFalse($location);
            $this->assertStringContainsString('/webmail/jabali-sso.php?token=', $location);
        } else {
            $response->assertStatus(200);
            $response->assertSee('Webmail Login Required');
            $response->assertSee('Open Webmail Login');
        }
    }

    public function test_webmail_sso_shows_reset_required_when_password_missing(): void
    {
        $user = User::factory()->create();
        $domain = Domain::factory()->for($user)->createQuietly([
            'domain' => 'reset-required.test',
        ]);
        $emailDomain = EmailDomain::create([
            'domain_id' => $domain->id,
            'is_active' => true,
        ]);
        $mailbox = Mailbox::create([
            'email_domain_id' => $emailDomain->id,
            'user_id' => $user->id,
            'local_part' => 'support',
            'password_hash' => '',
            'name' => 'Support',
        ]);

        $response = $this->actingAs($user)->get(route('webmail.sso', $mailbox));

        $response->assertStatus(200);
        $response->assertSee('Password Reset Required');
        $response->assertSee('Go to Email Settings');
    }
}

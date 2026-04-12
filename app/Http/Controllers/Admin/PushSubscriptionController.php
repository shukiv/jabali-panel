<?php

declare(strict_types=1);

namespace App\Http\Controllers\Admin;

use App\Http\Controllers\Controller;
use App\Models\PushSubscription;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * Per-admin Web Push subscription management. Lives under auth:admin.
 *
 *   POST   /jabali-admin/push-subscriptions      — register (upsert by endpoint)
 *   DELETE /jabali-admin/push-subscriptions      — drop the caller's active sub
 *   GET    /jabali-admin/push-subscriptions/vapid-public-key — fetch public key
 *
 * Routes are wired up by notifications.v2 into routes/admin.php.
 */
class PushSubscriptionController extends Controller
{
    public function store(Request $request): JsonResponse
    {
        $data = $request->validate([
            'endpoint' => ['required', 'string', 'url'],
            'keys.p256dh' => ['required', 'string', 'max:128'],
            'keys.auth' => ['required', 'string', 'max:64'],
        ]);

        $admin = $request->user('admin');
        abort_unless($admin, 401, 'admin auth required');

        $sub = PushSubscription::query()->updateOrCreate(
            [
                'admin_user_id' => $admin->getAuthIdentifier(),
                'endpoint' => $data['endpoint'],
            ],
            [
                'p256dh' => $data['keys']['p256dh'],
                'auth' => $data['keys']['auth'],
                'user_agent' => substr((string) $request->userAgent(), 0, 255),
                'last_used_at' => now(),
            ]
        );

        return response()->json(['id' => $sub->id, 'ok' => true], 201);
    }

    public function destroy(Request $request): JsonResponse
    {
        $admin = $request->user('admin');
        abort_unless($admin, 401, 'admin auth required');

        $endpoint = $request->input('endpoint');
        $query = PushSubscription::query()->where('admin_user_id', $admin->getAuthIdentifier());
        if (is_string($endpoint) && $endpoint !== '') {
            $query->where('endpoint', $endpoint);
        }
        $deleted = $query->delete();

        return response()->json(['deleted' => $deleted]);
    }

    public function vapidPublicKey(): JsonResponse
    {
        $key = (string) config('notifications.web_push.public_key');
        if ($key === '') {
            return response()->json([
                'error' => 'VAPID keypair not generated. Run `php artisan jabali:notifications:vapid --write-env` first.',
            ], 503);
        }

        return response()->json(['public_key' => $key]);
    }
}

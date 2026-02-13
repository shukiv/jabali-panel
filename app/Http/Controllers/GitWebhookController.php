<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Jobs\RunGitDeployment;
use App\Models\GitDeployment;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class GitWebhookController extends Controller
{
    public function __invoke(Request $request, GitDeployment $deployment, ?string $token = null): JsonResponse
    {
        $payload = $request->getContent();
        $providedSignature = (string) ($request->header('X-Jabali-Signature') ?? $request->header('X-Hub-Signature-256') ?? '');
        $providedSignature = preg_replace('/^sha256=/i', '', trim($providedSignature)) ?: '';
        $expectedSignature = hash_hmac('sha256', $payload, $deployment->secret_token);

        $hasValidSignature = $providedSignature !== '' && hash_equals($expectedSignature, $providedSignature);
        $hasValidLegacyToken = $token !== null && hash_equals($deployment->secret_token, $token);

        if (! $hasValidSignature && ! $hasValidLegacyToken) {
            return response()->json(['message' => 'Invalid token'], 403);
        }

        if (! $deployment->auto_deploy) {
            return response()->json(['message' => 'Auto-deploy disabled'], 202);
        }

        RunGitDeployment::dispatch($deployment->id);

        return response()->json(['message' => 'Deployment queued']);
    }
}

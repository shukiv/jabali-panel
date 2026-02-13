<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Models\Domain;
use App\Models\HostingPackage;
use App\Models\User;
use App\Services\Agent\AgentClient;
use App\Services\System\LinuxUserService;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Validator;

class AutomationApiController extends Controller
{
    public function listUsers(): JsonResponse
    {
        $users = User::query()
            ->where('is_admin', false)
            ->get(['id', 'name', 'username', 'email', 'is_active', 'hosting_package_id']);

        return response()->json(['users' => $users]);
    }

    public function createUser(Request $request): JsonResponse
    {
        $validator = Validator::make($request->all(), [
            'name' => ['required', 'string', 'max:255'],
            'username' => ['required', 'string', 'max:32', 'regex:/^[a-z][a-z0-9_]{0,31}$/', 'unique:users,username'],
            'email' => ['required', 'email', 'max:255', 'unique:users,email'],
            'password' => ['required', 'string', 'min:8'],
            'hosting_package_id' => ['nullable', 'exists:hosting_packages,id'],
        ]);

        if ($validator->fails()) {
            return response()->json(['errors' => $validator->errors()], 422);
        }

        $data = $validator->validated();
        $package = null;
        if (! empty($data['hosting_package_id'])) {
            $package = HostingPackage::find($data['hosting_package_id']);
        }

        $user = User::create([
            'name' => $data['name'],
            'username' => $data['username'],
            'email' => $data['email'],
            'password' => $data['password'],
            'sftp_password' => $data['password'],
            'is_admin' => false,
            'is_active' => true,
            'hosting_package_id' => $package?->id,
            'disk_quota_mb' => $package?->disk_quota_mb,
        ]);

        try {
            $linux = new LinuxUserService;
            $linux->createUser($user, $data['password']);
        } catch (\Exception $e) {
            $user->delete();

            return response()->json(['error' => $e->getMessage()], 500);
        }

        if ($package && $package->disk_quota_mb) {
            try {
                $agent = new AgentClient;
                $agent->quotaSet($user->username, (int) $package->disk_quota_mb);
            } catch (\Exception) {
                // keep user created, quota can be applied later
            }
        }

        return response()->json(['user' => $user], 201);
    }

    public function createDomain(Request $request): JsonResponse
    {
        $validator = Validator::make($request->all(), [
            'username' => ['nullable', 'string'],
            'user_id' => ['nullable', 'integer', 'exists:users,id'],
            'domain' => ['required', 'string'],
        ]);

        if ($validator->fails()) {
            return response()->json(['errors' => $validator->errors()], 422);
        }

        $data = $validator->validated();

        $user = null;
        if (! empty($data['user_id'])) {
            $user = User::where('id', $data['user_id'])->where('is_admin', false)->first();
        } elseif (! empty($data['username'])) {
            $user = User::where('username', $data['username'])->where('is_admin', false)->first();
        }

        if (! $user) {
            return response()->json(['error' => 'User not found'], 404);
        }

        $limit = $user->hostingPackage?->domains_limit;
        if ($limit && Domain::where('user_id', $user->id)->count() >= $limit) {
            return response()->json(['error' => 'Domain limit reached'], 409);
        }

        $agent = new AgentClient;
        $result = $agent->domainCreate($user->username, $data['domain']);
        if (! ($result['success'] ?? false)) {
            return response()->json(['error' => $result['error'] ?? 'Domain creation failed'], 500);
        }

        $domain = Domain::create([
            'user_id' => $user->id,
            'domain' => $data['domain'],
            'document_root' => '/home/'.$user->username.'/domains/'.$data['domain'].'/public_html',
            'is_active' => true,
            'ssl_enabled' => false,
            'directory_index' => 'index.php index.html',
            'page_cache_enabled' => false,
        ]);

        return response()->json(['domain' => $domain], 201);
    }
}

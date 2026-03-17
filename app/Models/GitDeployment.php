<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class GitDeployment extends Model
{
    protected $fillable = [
        'user_id',
        'domain_id',
        'repo_url',
        'branch',
        'deploy_path',
        'auto_deploy',
        'deploy_script',
        'last_status',
        'last_deployed_at',
        'last_error',
        'secret_token',
    ];

    protected function casts(): array
    {

        return [
            'auto_deploy' => 'boolean',
            'last_deployed_at' => 'datetime',
        ];

    }

    public function user(): BelongsTo
    {
        return $this->belongsTo(User::class);
    }

    public function domain(): BelongsTo
    {
        return $this->belongsTo(Domain::class);
    }
}

<?php

declare(strict_types=1);

namespace App\Models;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Facades\Crypt;

class MysqlCredential extends Model
{
    protected $fillable = ['user_id', 'mysql_username', 'mysql_password_encrypted'];

    public function setPasswordAttribute($value)
    {
        $this->attributes['mysql_password_encrypted'] = Crypt::encryptString($value);
    }

    public function getPasswordAttribute(): ?string
    {
        $encrypted = $this->attributes['mysql_password_encrypted'] ?? null;

        if ($encrypted === null || $encrypted === '') {
            return null;
        }

        return Crypt::decryptString($encrypted);
    }
}

<?php

declare(strict_types=1);

namespace Tests\Unit;

use Tests\TestCase;

class ClamavSignatureCountingTest extends TestCase
{
    public function test_custom_signature_file_types_are_counted(): void
    {
        $script = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($script);

        $expectedPatterns = [
            '*.ndb',
            '*.hdb',
            '*.ldb',
            '*.cdb',
            '*.hsb',
            '*.ftm',
            '*.ign2',
        ];

        foreach ($expectedPatterns as $pattern) {
            $this->assertStringContainsString($pattern, $script);
        }
    }

    public function test_binary_signatures_use_hex_marker(): void
    {
        $script = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($script);

        $expected = [
            'Jabali.Shell.JPEG:0:0:HEX:FFD8FFE0{-200}3C3F706870',
            'Jabali.SuspExt.PHP.JPG:0:0:HEX:FFD8FFE0{-500}3C3F706870',
            'Jabali.SuspExt.PHP.PNG:0:0:HEX:89504E47{-500}3C3F706870',
            'Jabali.SuspExt.PHP.GIF:0:0:HEX:47494638{-500}3C3F706870',
            'Jabali.SuspExt.PHP.PDF:0:0:HEX:25504446{-500}3C3F706870',
        ];

        foreach ($expected as $line) {
            $this->assertStringContainsString($line, $script);
        }
    }

    public function test_negative_skip_tokens_are_normalized(): void
    {
        $script = file_get_contents(base_path('bin/jabali-agent'));

        $this->assertIsString($script);
        $this->assertStringContainsString('{0-', $script);
        $this->assertStringContainsString('preg_match(\'/\\{[-0-9]+(?:-[0-9]+)?\\}$/\'', $script);
    }
}

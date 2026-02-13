<div class="overflow-hidden rounded-lg border border-gray-700">
    <div class="max-h-[400px] overflow-auto bg-[#0d1117] font-mono fi-section-header-description leading-relaxed">
        <table class="w-full border-collapse">
            <tbody>
                @foreach(explode("\n", $log) as $index => $line)
                <tr class="hover:bg-[#161b22]">
                    <td class="w-1 whitespace-nowrap bg-[#161b22] px-3 text-right fi-color-gray fi-text-color-500 select-none align-top border-r border-[#30363d]">{{ $index + 1 }}</td>
                    <td class="px-3 fi-color-gray fi-text-color-200 whitespace-pre-wrap break-all">{{ $line }}</td>
                </tr>
                @endforeach
            </tbody>
        </table>
    </div>
</div>

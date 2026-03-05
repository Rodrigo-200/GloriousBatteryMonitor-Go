Add-Type -AssemblyName System.Drawing

function New-GBitmap([int]$size) {
    $bmp = New-Object System.Drawing.Bitmap($size, $size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = 'AntiAlias'
    $g.Clear([System.Drawing.Color]::FromArgb(0x11, 0x13, 0x18))

    $pw = [Math]::Max(2, [int]($size * 0.14))
    $pad = [int]($size * 0.10) + [int]($pw / 2)

    $arcPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(0xE2, 0xE8, 0xF0), $pw)
    $arcPen.StartCap = 'Round'
    $arcPen.EndCap = 'Round'
    $arcRect = New-Object System.Drawing.Rectangle($pad, $pad, ($size - 2 * $pad), ($size - 2 * $pad))
    $g.DrawArc($arcPen, $arcRect, 0, 300)

    $tealPen = New-Object System.Drawing.Pen([System.Drawing.Color]::FromArgb(0x00, 0xE5, 0xCC), $pw)
    $tealPen.StartCap = 'Round'
    $tealPen.EndCap = 'Round'
    $midY = [int]($size / 2)
    $barLeft = [int]($size / 2)
    $barRight = $size - $pad
    $g.DrawLine($tealPen, $barLeft, $midY, $barRight, $midY)

    $arcPen.Dispose()
    $tealPen.Dispose()
    $g.Dispose()
    return $bmp
}

$sizes = @(16, 32, 48, 256)
$pngArrays = @()
$bitmaps = @()

foreach ($s in $sizes) {
    $bmp = New-GBitmap $s
    $bitmaps += $bmp
    $ms = New-Object System.IO.MemoryStream
    $bmp.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
    $pngArrays += , $ms.ToArray()
    $ms.Dispose()
}

$outPath = Join-Path $PSScriptRoot 'src\GBM.Desktop\Assets\app-icon.ico'
$fs = New-Object System.IO.MemoryStream
$w = New-Object System.IO.BinaryWriter($fs)

# ICO Header
$w.Write([UInt16]0)          # Reserved
$w.Write([UInt16]1)          # Type = ICO
$w.Write([UInt16]$sizes.Length)  # Image count

$dataOffset = 6 + ($sizes.Length * 16)

for ($i = 0; $i -lt $sizes.Length; $i++) {
    $sz = if ($sizes[$i] -ge 256) { 0 } else { $sizes[$i] }
    $w.Write([byte]$sz)       # Width
    $w.Write([byte]$sz)       # Height
    $w.Write([byte]0)         # Color palette
    $w.Write([byte]0)         # Reserved
    $w.Write([UInt16]1)       # Color planes
    $w.Write([UInt16]32)      # Bits per pixel
    $w.Write([UInt32]$pngArrays[$i].Length)  # Image data size
    $w.Write([UInt32]$dataOffset)            # Image data offset
    $dataOffset += $pngArrays[$i].Length
}

foreach ($png in $pngArrays) {
    $w.Write($png)
}

[System.IO.File]::WriteAllBytes($outPath, $fs.ToArray())
$w.Dispose()
$fs.Dispose()

foreach ($bmp in $bitmaps) { $bmp.Dispose() }

$info = Get-Item $outPath
Write-Host ("Icon created: {0} bytes at {1}" -f $info.Length, $outPath)

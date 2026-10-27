$lock=$null
$cacheDir=Join-Path $base 'cache'
$cache=Join-Path $cacheDir ($digest+'.bin')
$part=Join-Path $cacheDir ($digest+'.part')
$next=Join-Path $stage 'agent.exe'
$current=Join-Path $base 'agent.exe'
function Test-Artifact($p) {
  if (!(Test-Path -LiteralPath $p -PathType Leaf)) { return $false }
  Assert-Path $p
  return ((Get-Item -LiteralPath $p).Length -eq $size -and (Get-FileHash -Algorithm SHA256 -LiteralPath $p).Hash -eq $digest)
}
function Publish-Artifact {
  Move-Item -LiteralPath $part -Destination $cache -Force
  Copy-Item -LiteralPath $cache -Destination $next -Force
  [Console]::WriteLine('TALUS_ARTIFACT_READY')
}
try {
  Assert-Admin
  $lock=Lock-Install
  Assert-Path $stage
  if (!(Test-Path -LiteralPath $stage -PathType Container)) { throw 'Installation removed' }
  Protect-Directory $cacheDir
  foreach ($p in @($cache,$part,$next,$current,$fetchConfig)) { Assert-Path $p }
  Get-ChildItem -LiteralPath $cacheDir -File -Force | Where-Object { $_.FullName -ne [IO.Path]::GetFullPath($cache) -and $_.FullName -ne [IO.Path]::GetFullPath($part) } | ForEach-Object { Assert-Path $_.FullName; Remove-Item -LiteralPath $_.FullName -Force }
  if ($mode -eq 'store') {
    if (!(Test-Artifact $next)) { exit 77 }
    Copy-Item -LiteralPath $next -Destination $part -Force
    Publish-Artifact
    exit 0
  }
  if (Test-Artifact $cache) { Copy-Item -LiteralPath $cache -Destination $next -Force; [Console]::WriteLine('TALUS_ARTIFACT_READY'); exit 0 }
  if (Test-Artifact $current) { Copy-Item -LiteralPath $current -Destination $part -Force; Publish-Artifact; exit 0 }
  if (Test-Artifact $part) { Publish-Artifact; exit 0 }
  if (Test-Path -LiteralPath $cache) { Remove-Item -LiteralPath $cache -Force }
  if ((Test-Path -LiteralPath $part) -and (Get-Item -LiteralPath $part).Length -ge $size) { Remove-Item -LiteralPath $part -Force }
  $settings=Get-Content -LiteralPath $fetchConfig -Raw | ConvertFrom-Json
  $uri=[Uri]$settings.url
  if ($uri.UserInfo -or $uri.Query -or $uri.Fragment -or ($uri.Scheme -ne 'https' -and !($settings.allow_http -and $uri.Scheme -eq 'http'))) { throw 'Invalid artifact URL' }
  [Net.ServicePointManager]::SecurityProtocol=[Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  $clock=[Diagnostics.Stopwatch]::StartNew()
  for ($attempt=0; $attempt -lt 2; $attempt++) {
    $offset=0L
    if (Test-Path -LiteralPath $part) { $offset=(Get-Item -LiteralPath $part).Length }
    $response=$null; $stream=$null; $file=$null
    try {
      $request=[Net.HttpWebRequest]::Create($uri)
      $request.AllowAutoRedirect=$false
      $remaining=[int][Math]::Ceiling(($seconds-$clock.Elapsed.TotalSeconds)*1000)
      if ($remaining -le 0) { throw 'Artifact transfer deadline reached' }
      $request.Timeout=[Math]::Min(10000,$remaining)
      $request.ReadWriteTimeout=30000
      $request.Headers['Authorization']='Bearer '+$settings.token
      $request.Headers['Accept-Encoding']='identity'
      $request.Headers['If-Range']='"'+$digest+'"'
      if ($offset -gt 0) { $request.AddRange($offset) }
      try { $response=$request.GetResponse() }
      catch [Net.WebException] {
        if ($_.Exception.Response -and [int]$_.Exception.Response.StatusCode -in @(429,503)) {
          $_.Exception.Response.Close(); [Console]::WriteLine('TALUS_PULL_BUSY'); exit 79
        }
        if ($_.Exception.Response -and [int]$_.Exception.Response.StatusCode -eq 416 -and $offset -gt 0) {
          $_.Exception.Response.Close(); Remove-Item -LiteralPath $part -Force; continue
        }
        throw
      }
      $status=[int]$response.StatusCode
      if ($status -notin @(200,206)) { throw 'Artifact HTTP path unavailable' }
      if ($offset -gt 0 -and ($status -ne 206 -or $response.Headers['ETag'] -ne ('"'+$digest+'"'))) {
        Remove-Item -LiteralPath $part -Force; continue
      }
      if (($offset -eq 0 -and $status -ne 200) -or ($offset -gt 0 -and $response.Headers['Content-Range'] -ne ('bytes '+$offset+'-'+($size-1)+'/'+$size))) { throw 'Invalid artifact range response' }
      $stream=$response.GetResponseStream()
      $file=[IO.File]::Open($part,[IO.FileMode]::Append,[IO.FileAccess]::Write,[IO.FileShare]::None)
      $buffer=New-Object byte[] 65536
      $written=$offset
      while ($true) {
        $remaining=[int][Math]::Ceiling(($seconds-$clock.Elapsed.TotalSeconds)*1000)
        if ($remaining -le 0) { throw 'Artifact transfer deadline reached' }
        $read=$stream.ReadAsync($buffer,0,$buffer.Length)
        if (!$read.Wait([Math]::Min(30000,$remaining))) {
          $request.Abort(); throw 'Artifact transfer read deadline reached'
        }
        $count=$read.GetAwaiter().GetResult()
        if ($count -eq 0) { break }
        if ($written+$count -gt $size) { throw 'Artifact exceeds expected size' }
        $file.Write($buffer,0,$count); $written+=$count
        if ($clock.Elapsed.TotalSeconds -ge $seconds) { throw 'Artifact transfer deadline reached' }
      }
      $file.Dispose(); $file=$null
      if ($written -ne $size) { throw 'Incomplete artifact response' }
      if (!(Test-Artifact $part)) { Remove-Item -LiteralPath $part -Force; exit 77 }
      Publish-Artifact
      exit 0
    } catch {
      if ($file) { $file.Dispose(); $file=$null }
      if ((Test-Path -LiteralPath $part) -and (Get-Item -LiteralPath $part).Length -gt $offset) {
        [Console]::WriteLine('TALUS_PULL_INCOMPLETE'); exit 76
      }
      [Console]::WriteLine('TALUS_PULL_UNAVAILABLE'); exit 75
    } finally {
      if ($file) { $file.Dispose() }
      if ($stream) { $stream.Dispose() }
      if ($response) { $response.Close() }
    }
  }
  [Console]::WriteLine('TALUS_PULL_UNAVAILABLE'); exit 75
} finally {
  if (Test-Path -LiteralPath $fetchConfig) { Remove-Item -LiteralPath $fetchConfig -Force -ErrorAction SilentlyContinue }
  if ($lock) { $lock.Dispose() }
}

target "native-cache" {
  inherits = ["build"]
  target = "base"
  tags = []
  output = ["type=image,push=false,store=false"]
  attest = ["type=provenance,disabled=true"]
}

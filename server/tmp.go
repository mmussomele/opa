package server

func NewSecureServer(cert *tls.Certificate) grpcServer {
	creds := credentials.NewServerTLSFromCert(cert)
	server := grpc.NewServer(grpc.WithTransportCredentials(creds))
	opaServer := grpcServer{}
	proto.RegisterOPAServer(server, opaServer)
}

func NewInsecureServer() grpcServer {
	return grpcServer{
		server: grpc.NewServer(),
	}
}

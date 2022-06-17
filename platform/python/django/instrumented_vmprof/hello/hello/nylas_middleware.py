import vmprof

class NylasMiddleware:
    def __init__(self, get_response):
        self.get_response = get_response
    
    def __call__(self, request):
        with open('vmprof_output', 'w+') as f:
            vmprof.enable(f.fileno(), 0.01)
            response = self.get_response(request)
            vmprof.disable()
        
        return response